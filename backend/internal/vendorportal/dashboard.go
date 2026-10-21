package vendorportal

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/clock"
	"purchasedesk/internal/database"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/invoices"
	"purchasedesk/internal/money"
)

// entry is one line of the vendor's statement. A bill adds to what Saltstayz owes; TDS held on it, a payment and an
// advance paid take from it; an advance the vendor returned adds back. Balance runs over the whole history.
type entry struct {
	Date     string  `json:"date"`
	Kind     string  `json:"kind"` // bill, tds, payment, advance, refund
	Text     string  `json:"text"`
	Ref      string  `json:"ref"` // invoice, advance or payment reference
	PONumber string  `json:"po_number"`
	POValue  float64 `json:"po_value"` // the PO's value incl. GST, shown next to its number
	Property string  `json:"property"`
	Bill     float64 `json:"bill"`
	Paid     float64 `json:"paid"`
	Balance  float64 `json:"balance"`
	Status   string  `json:"status"`
}

// kindOrder puts a day's bills before what was paid against them.
var kindOrder = map[string]int{"opening": -1, "bill": 0, "tds": 1, "advance": 2, "payment": 3, "refund": 4}

type waitingOrder struct {
	ID       int64     `json:"id"`
	PONumber string    `json:"po_number"`
	PODate   time.Time `json:"po_date"`
	Property string    `json:"property"`
	Total    float64   `json:"total"`
}

// dashboard is the vendor's home page: what waits for it, where its money stands, and its statement.
func (s *Service) dashboard(w http.ResponseWriter, r *http.Request) error {
	v := from(r)
	ctx := r.Context()
	list, err := invoices.ForVendor(ctx, s.DB, v.ID, true)
	if err != nil {
		return err
	}
	out := struct {
		NewOrders      int            `json:"new_orders"`      // sent, not accepted or declined yet
		ToDeliver      int            `json:"to_deliver"`      // accepted, still open
		Checking       int            `json:"checking"`        // invoices being checked
		CheckingAmount float64        `json:"checking_amount"` //
		Approved       float64        `json:"approved"`        // approved for payment, not paid yet (what the vendor gets)
		NextPayBy      string         `json:"next_pay_by"`     // the earliest pay-by date among them
		PaidThisMonth  float64        `json:"paid_this_month"` // payments and advances paid this month
		Balance        float64        `json:"balance"`         // what Saltstayz owes now (negative: advance not yet billed)
		Waiting        []waitingOrder `json:"waiting"`         // the new orders, newest first
		Statement      []entry        `json:"statement"`       // oldest first, with the running balance
	}{Waiting: []waitingOrder{}, Statement: []entry{}}

	if err := s.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE d.status='PO_ISSUED' AND d.vendor_status=''),
		count(*) FILTER (WHERE d.status='PO_ISSUED' AND d.vendor_status='accepted') FROM document d WHERE `+ownPO, v.ID).Scan(&out.NewOrders, &out.ToDeliver); err != nil {
		return err
	}
	rows, err := s.DB.Query(ctx, `SELECT d.id, d.po_number, d.po_date, p.name, coalesce((SELECT sum(`+lineValue+`) FROM document_line l WHERE l.document_id=d.id),0)::float8
		FROM document d JOIN property p ON p.id=d.property_id WHERE `+ownPO+` AND d.status='PO_ISSUED' AND d.vendor_status='' ORDER BY d.po_date DESC LIMIT 10`, v.ID)
	if err != nil {
		return err
	}
	if out.Waiting, err = pgx.CollectRows(rows, pgx.RowToStructByPos[waitingOrder]); err != nil {
		return err
	}

	for _, x := range list {
		switch x.Status {
		case "RECEIVED", "MATCHED", "AWAITING":
			out.Checking++
			out.CheckingAmount += x.Amount
		case "PAY_APPROVED", "PUSHED":
			out.Approved += x.NetPayable
			if x.PayBy != "" && (out.NextPayBy == "" || x.PayBy < out.NextPayBy) {
				out.NextPayBy = x.PayBy
			}
		}
	}
	st, bal, err := statementOf(ctx, s.DB, v.ID, list)
	if err != nil {
		return err
	}
	monthStart := clock.YMD(clock.MonthStart(clock.Now()))
	for _, e := range st {
		if (e.Kind == "payment" || e.Kind == "advance") && e.Date >= monthStart {
			out.PaidThisMonth += e.Paid
		}
	}
	out.Statement, out.Balance = st, bal
	out.CheckingAmount, out.Approved, out.PaidThisMonth = money.Round2(out.CheckingAmount), money.Round2(out.Approved), money.Round2(out.PaidThisMonth)
	return httpjson.OK(w, out)
}

// Balance is a vendor's running balance now, the same figure as the end of its statement: bills less what was paid
// (negative: advances, the opening one included, not yet billed). Approvers see it on the request.
func Balance(ctx context.Context, q database.Q, vendorID int64) (float64, error) {
	list, err := invoices.ForVendor(ctx, q, vendorID, true)
	if err != nil {
		return 0, err
	}
	_, bal, err := statementOf(ctx, q, vendorID, list)
	return bal, err
}

// statementOf builds the statement, oldest first, with the running balance: the opening balance from before Purchase
// Desk, each bill (with the TDS held on it), each payment (net of the advance set off), each advance paid and returned.
func statementOf(ctx context.Context, q database.Q, vendorID int64, list []invoices.Invoice) ([]entry, float64, error) {
	st := []entry{}
	var opening float64
	var asOn string
	if err := q.QueryRow(ctx, `SELECT opening_balance::float8, coalesce(opening_as_on::text, created_at::date::text) FROM vendor WHERE id=$1`, vendorID).Scan(&opening, &asOn); err != nil {
		return nil, 0, err
	}
	if opening != 0 {
		e := entry{Date: asOn, Kind: "opening", Text: "Opening balance (before Purchase Desk)", Status: "Opening"}
		if opening < 0 {
			e.Paid, e.Text = -opening, "Opening balance: advance already paid"
		} else {
			e.Bill, e.Text = opening, "Opening balance: earlier bills due"
		}
		st = append(st, e)
	}
	poValue := map[string]float64{}
	rows, err := q.Query(ctx, `SELECT d.po_number, coalesce(sum(`+lineValue+`),0)::float8 FROM document d JOIN document_line l ON l.document_id=d.id
		WHERE d.vendor_id=$1 AND d.po_number IS NOT NULL GROUP BY d.po_number`, vendorID)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var po string
		var v float64
		if err := rows.Scan(&po, &v); err != nil {
			rows.Close()
			return nil, 0, err
		}
		poValue[po] = money.Round2(v)
	}
	rows.Close()
	for _, x := range list {
		stg := stage(x)
		st = append(st, entry{Date: x.Date, Kind: "bill", Text: "Bill " + x.Number, Ref: x.Number, PONumber: x.PONumber, Property: x.Property, Bill: x.Amount, Status: stg})
		if x.TDS > 0 {
			st = append(st, entry{Date: x.Date, Kind: "tds", Text: "TDS " + x.TDSSections + " held on bill " + x.Number, Ref: x.Number, PONumber: x.PONumber, Property: x.Property, Paid: x.TDS, Status: stg})
		}
		if x.Status == "PAID" {
			if x.PaidAt != nil {
				text := "Payment for bill " + x.Number
				if adv := money.Round2(x.AdvanceUsed - x.AdvanceTDS); adv > 0 {
					text += ", after advance set off " + money.INR(adv)
				}
				st = append(st, entry{Date: clock.YMD(*x.PaidAt), Kind: "payment", Text: text, Ref: x.PaymentRef, PONumber: x.PONumber, Property: x.Property, Paid: x.NetPayable, Status: "Paid"})
			}
		}
	}
	// Advances paid (the money sent, after the TDS held on them), and what the vendor returned from them.
	rows, err = q.Query(ctx, `SELECT 'advance', a.paid_on::text, a.number, coalesce(d.po_number,''), p.name, (a.amount-a.tds_amount)::float8, a.tds_amount::float8, a.payment_ref
		FROM advance a JOIN document d ON d.id=a.document_id JOIN property p ON p.id=a.property_id WHERE a.vendor_id=$1 AND a.status='PAID' AND a.paid_on IS NOT NULL
		UNION ALL
		SELECT 'refund', u.used_on::text, a.number, coalesce(d.po_number,''), p.name, (u.amount-u.tds_amount)::float8, 0::float8, u.reference
		FROM advance_use u JOIN advance a ON a.id=u.advance_id JOIN document d ON d.id=a.document_id JOIN property p ON p.id=a.property_id
		WHERE a.vendor_id=$1 AND u.kind='refund'`, vendorID)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var kind, date, number, po, prop, ref string
		var amt, tds float64
		if err := rows.Scan(&kind, &date, &number, &po, &prop, &amt, &tds, &ref); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if kind == "advance" {
			text := "Advance " + number + " paid"
			if tds > 0 {
				text += ", after TDS " + money.INR(tds)
			}
			st = append(st, entry{Date: date, Kind: "advance", Text: text, Ref: ref, PONumber: po, Property: prop, Paid: amt, Status: "Paid"})
		} else {
			st = append(st, entry{Date: date, Kind: "refund", Text: "Advance " + number + " returned by you", Ref: ref, PONumber: po, Property: prop, Paid: -amt, Status: "Returned"})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	sort.SliceStable(st, func(i, j int) bool {
		if st[i].Date != st[j].Date {
			return st[i].Date < st[j].Date
		}
		return kindOrder[st[i].Kind] < kindOrder[st[j].Kind]
	})
	bal := 0.0
	for i := range st {
		bal = money.Round2(bal + st[i].Bill - st[i].Paid)
		st[i].Balance = bal
	}
	for i := range st {
		st[i].POValue = poValue[st[i].PONumber]
	}
	return st, bal, nil
}
