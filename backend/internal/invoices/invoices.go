// Package invoices runs everything after goods are received: the three-way match by Accounts Payable,
// payment approval by Finance (due date from payment terms, MSME 45-day limit), and the fallback "mark paid".
// One invoice is one payable and becomes one bill in Zoho Books.
package invoices

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/advances"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/database"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/money"
	"purchasedesk/internal/screens"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/zohowake"
)

type Service struct {
	DB    *pgxpool.Pool
	Guard *access.Guard
	// Held returns, per invoice, what stops it being pushed to Zoho. Set by the Zoho push module.
	Held func(ctx context.Context, ids []int64) (map[int64][]string, error)
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/invoices", httpjson.Wrap(s.list))
	r.Get("/invoices/{id}", httpjson.Wrap(s.get))
	r.Post("/invoices/bulk", httpjson.Wrap(s.bulk))
	r.Post("/invoices/{id}/match", httpjson.Wrap(s.match))
	r.Post("/invoices/{id}/send-back", httpjson.Wrap(s.sendBack))
	r.Post("/invoices/{id}/approve-payment", httpjson.Wrap(s.approvePayment))
}

type Invoice struct {
	ID            int64          `json:"id"`
	DocumentID    int64          `json:"document_id"`
	DocNumber     string         `json:"document_number"`
	Kind          string         `json:"kind"`
	PONumber      string         `json:"po_number"`
	GRN           string         `json:"grn_number"`
	DeliveryNo    int            `json:"delivery_no"`
	ReceivedAt    *time.Time     `json:"received_at"`
	VendorID      int64          `json:"vendor_id"`
	Vendor        string         `json:"vendor"`
	VendorMSME    bool           `json:"vendor_msme"`
	VendorGST     bool           `json:"vendor_gst_registered"`
	Terms         string         `json:"payment_terms"`
	PropertyID    int64          `json:"property_id"`
	Property      string         `json:"property"`
	ClusterID     int64          `json:"cluster_id"`
	City          string         `json:"city"`
	BillType      string         `json:"bill_type"`
	Number        string         `json:"invoice_number"`
	Voucher       string         `json:"voucher_number"` // purchase voucher PV_0001: one series for every bill entered
	Date          string         `json:"invoice_date"`
	Amount        float64        `json:"amount"`
	GST           *float64       `json:"gst_pct"`
	FileID        *int64         `json:"file_id"`
	Status        string         `json:"status"`
	MatchRemark   string         `json:"match_remark"`
	DueDate       string         `json:"due_date"`
	PayBy         string         `json:"pay_by"`
	MSMELimit     string         `json:"msme_limit"`
	ZohoBillID    string         `json:"zoho_bill_id"`
	ZohoStatus    string         `json:"zoho_status"`
	ZohoBalance   *float64       `json:"zoho_balance"`
	ZohoPaid      *float64       `json:"zoho_paid"`
	ZohoError     string         `json:"zoho_error"`
	PaidAt        *time.Time     `json:"paid_at"`
	PaymentRef    string         `json:"payment_ref"`
	OrderedValue  float64        `json:"ordered_value"`
	ReceivedValue float64        `json:"received_value"`
	Difference    float64        `json:"difference"`
	WithinTol     bool           `json:"within_tolerance"`
	Held          []string       `json:"held"`
	AdvanceWait   []string       `json:"advance_wait"`     // advances on the PO Finance has not settled: payment approval waits for them
	TDS           float64        `json:"tds_amount"`       // tax deducted at source on service lines, on the value before GST
	TDSSections   string         `json:"tds_sections"`     // the sections it falls under, for example "194C"
	NetPayable    float64        `json:"net_payable"`      // what the vendor is paid: amount less TDS, less the advance adjusted
	AdvanceUsed   float64        `json:"advance_adjusted"` // advances adjusted on this invoice (including the TDS held on them)
	AdvanceTDS    float64        `json:"advance_tds"`      // TDS already held when those advances were paid
	AdvanceUses   []advances.Use `json:"advance_uses,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	Lines         []RLine        `json:"lines,omitempty"`
}

// RLine is one line of a goods receipt: ordered, received in this delivery, and its value.
type RLine struct {
	LineID   int64   `json:"line_id"`
	SKUCode  string  `json:"sku_code"`
	Item     string  `json:"item"`
	PackSize string  `json:"pack_size"`
	HSN      string  `json:"hsn"`
	Ordered  float64 `json:"ordered"`
	Received float64 `json:"received"`
	Rate     float64 `json:"rate"`
	GST      float64 `json:"gst_pct"`
	Value    float64 `json:"value"`
}

const selectInvoice = `SELECT i.id, i.document_id, d.number, d.kind, coalesce(d.po_number,''), coalesce(g.number,''), coalesce(g.delivery_no,0), g.received_at,
	i.vendor_id, v.legal_name, v.msme, v.gst_registered, coalesce(nullif(d.payment_terms,''), v.payment_terms), i.property_id, p.name, p.cluster_id, c.name, coalesce(bt.name, eh.name, ''),
	i.invoice_number, i.voucher_number, i.invoice_date::text, i.amount, i.gst_pct, i.file_id, i.status, i.match_remark, coalesce(i.due_date::text,''),
	coalesce(i.zoho_bill_id,''), i.zoho_status, i.zoho_balance, i.zoho_paid, i.zoho_error, i.paid_at, i.payment_ref, i.created_at,
	coalesce((SELECT sum(l.qty*l.rate*(1+l.gst_pct/100)) FROM goods_receipt_line gl JOIN document_line l ON l.id=gl.line_id WHERE gl.goods_receipt_id=g.id),0),
	coalesce((SELECT sum(gl.qty*l.rate*(1+l.gst_pct/100)) FROM goods_receipt_line gl JOIN document_line l ON l.id=gl.line_id WHERE gl.goods_receipt_id=g.id),0),
	coalesce((SELECT sum(gl.qty*l.rate*l.tds_pct/100) FROM goods_receipt_line gl JOIN document_line l ON l.id=gl.line_id WHERE gl.goods_receipt_id=g.id),0),
	coalesce((SELECT string_agg(DISTINCT l.tds_section, ', ') FROM goods_receipt_line gl JOIN document_line l ON l.id=gl.line_id WHERE gl.goods_receipt_id=g.id AND l.tds_pct>0),''),
	coalesce((SELECT sum(x.amount) FROM advance_use x WHERE x.invoice_id=i.id),0)::float8, coalesce((SELECT sum(x.tds_amount) FROM advance_use x WHERE x.invoice_id=i.id),0)::float8
	FROM invoice i JOIN document d ON d.id=i.document_id JOIN vendor v ON v.id=i.vendor_id JOIN property p ON p.id=i.property_id
	JOIN cluster k ON k.id=p.cluster_id JOIN city c ON c.id=k.city_id
	LEFT JOIN goods_receipt g ON g.id=i.goods_receipt_id LEFT JOIN bill_type bt ON bt.id=d.bill_type_id LEFT JOIN expense_head eh ON eh.id=d.expense_head_id`

func scan(st flowsettings.Settings) func(pgx.CollectableRow) (Invoice, error) {
	return func(row pgx.CollectableRow) (Invoice, error) {
		var x Invoice
		err := row.Scan(&x.ID, &x.DocumentID, &x.DocNumber, &x.Kind, &x.PONumber, &x.GRN, &x.DeliveryNo, &x.ReceivedAt,
			&x.VendorID, &x.Vendor, &x.VendorMSME, &x.VendorGST, &x.Terms, &x.PropertyID, &x.Property, &x.ClusterID, &x.City, &x.BillType,
			&x.Number, &x.Voucher, &x.Date, &x.Amount, &x.GST, &x.FileID, &x.Status, &x.MatchRemark, &x.DueDate,
			&x.ZohoBillID, &x.ZohoStatus, &x.ZohoBalance, &x.ZohoPaid, &x.ZohoError, &x.PaidAt, &x.PaymentRef, &x.CreatedAt,
			&x.OrderedValue, &x.ReceivedValue, &x.TDS, &x.TDSSections, &x.AdvanceUsed, &x.AdvanceTDS)
		if err != nil {
			return x, err
		}
		// TDS is rounded to the nearest rupee (Income Tax Act, section 288B) and taken off what the vendor is paid.
		x.TDS = math.Round(x.TDS)
		x.NetPayable = money.Round2(x.Amount - x.TDS - (x.AdvanceUsed - x.AdvanceTDS))
		x.OrderedValue, x.ReceivedValue = money.Round2(x.OrderedValue), money.Round2(x.ReceivedValue)
		bill := x.Kind == "bill" || x.Kind == "corp_bill"
		if bill {
			x.ReceivedValue = x.Amount
		}
		x.Difference = money.Round2(x.Amount - x.ReceivedValue)
		x.WithinTol = bill || abs(x.Difference) <= st.MatchTolPct/100*x.ReceivedValue
		x.PayBy, x.MSMELimit = payBy(x, st)
		x.Held = []string{}
		x.AdvanceWait = []string{}
		return x, nil
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// payBy: the due date from the invoice date and the payment terms (money.DueDate); an MSME vendor must also be paid within the MSME limit from receipt.
func payBy(x Invoice, st flowsettings.Settings) (string, string) {
	if x.DueDate != "" && !x.VendorMSME {
		return x.DueDate, ""
	}
	base, ok := clock.ParseDate(x.Date)
	if !ok {
		return "", ""
	}
	due := money.DueDate(base, x.Terms)
	limit := ""
	if x.VendorMSME {
		recv := x.CreatedAt
		if x.ReceivedAt != nil {
			recv = *x.ReceivedAt
		}
		lim := clock.DayOf(recv).AddDate(0, 0, st.MSMEDays)
		limit = clock.YMD(lim)
		if lim.Before(due) {
			due = lim
		}
	}
	return clock.YMD(due), limit
}

// List returns invoices in the user's scope. status may be empty or a comma list.
func (s *Service) List(ctx context.Context, u *access.User, statuses []string, docID int64) ([]Invoice, error) {
	all, ids, err := s.Guard.Visible(ctx, u)
	if err != nil {
		return nil, err
	}
	st, err := flowsettings.Load(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, selectInvoice+` WHERE ($1 OR i.property_id = ANY($2)) AND (cardinality($3::text[])=0 OR i.status = ANY($3)) AND ($4=0 OR i.document_id=$4)
		AND i.status NOT IN ('AWAITING') AND (i.status<>'PAID' OR i.paid_at > now() - interval '120 days' OR $4<>0) ORDER BY i.created_at DESC`, all, ids, statuses, docID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, scan(st))
	if err != nil {
		return nil, err
	}
	if err := withAdvanceWait(ctx, s.DB, out); err != nil {
		return nil, err
	}
	if s.Held != nil {
		check := []int64{}
		for _, x := range out {
			if x.Status == documents.InvPayApproved {
				check = append(check, x.ID)
			}
		}
		if len(check) > 0 {
			held, err := s.Held(ctx, check)
			if err != nil {
				return nil, err
			}
			for i := range out {
				if h := held[out[i].ID]; h != nil {
					out[i].Held = h
				}
			}
		}
	}
	return out, nil
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	sts := []string{}
	if v := r.URL.Query().Get("status"); v != "" {
		sts = strings.Split(v, ",")
	}
	out, err := s.List(r.Context(), access.From(r), sts, int64(httpjson.QInt(r, "document_id", 0)))
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

// ForVendor lists one vendor's invoices for the vendor portal, newest first: everything not voided, paid ones from the
// last year (all of them when all is set, for the vendor's statement).
func ForVendor(ctx context.Context, q database.Q, vendorID int64, all bool) ([]Invoice, error) {
	st, err := flowsettings.Load(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, selectInvoice+` WHERE i.vendor_id=$1 AND i.status<>'VOID' AND ($2 OR i.status<>'PAID' OR i.paid_at > now() - interval '365 days')
		ORDER BY i.created_at DESC LIMIT 5000`, vendorID, all)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scan(st))
}

// Load reads one invoice with its receipt lines.
func Load(ctx context.Context, q database.Q, id int64) (*Invoice, error) {
	st, err := flowsettings.Load(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, selectInvoice+` WHERE i.id=$1`, id)
	if err != nil {
		return nil, err
	}
	x, err := pgx.CollectOneRow(rows, scan(st))
	if err == pgx.ErrNoRows {
		return nil, httpjson.NotFound("Invoice")
	}
	if err != nil {
		return nil, err
	}
	lr, err := q.Query(ctx, `SELECT l.id, coalesce(k.code,''), coalesce(concat_ws(' ', b.name, k.item_name), l.description), coalesce(ps.name, l.unit), coalesce(k.hsn,''), l.qty, gl.qty, l.rate, l.gst_pct
		FROM invoice i JOIN goods_receipt_line gl ON gl.goods_receipt_id=i.goods_receipt_id JOIN document_line l ON l.id=gl.line_id
		LEFT JOIN sku k ON k.id=l.sku_id LEFT JOIN brand b ON b.id=k.brand_id LEFT JOIN pack_size ps ON ps.id=k.pack_size_id WHERE i.id=$1 ORDER BY l.id`, id)
	if err != nil {
		return nil, err
	}
	x.Lines, err = pgx.CollectRows(lr, func(row pgx.CollectableRow) (RLine, error) {
		var l RLine
		err := row.Scan(&l.LineID, &l.SKUCode, &l.Item, &l.PackSize, &l.HSN, &l.Ordered, &l.Received, &l.Rate, &l.GST)
		l.Value = money.Round2(money.LineValue(l.Received, l.Rate, l.GST))
		return l, err
	})
	if err != nil {
		return nil, err
	}
	if x.AdvanceUses, err = advances.UsesForInvoice(ctx, q, id); err != nil {
		return nil, err
	}
	one := []Invoice{x}
	err = withAdvanceWait(ctx, q, one)
	return &one[0], err
}

// withAdvanceWait marks invoices still before payment approval whose PO has an advance Finance has not settled.
func withAdvanceWait(ctx context.Context, q database.Q, list []Invoice) error {
	docs := []int64{}
	for _, x := range list {
		if x.Status == documents.InvReceived || x.Status == documents.InvMatched {
			docs = append(docs, x.DocumentID)
		}
	}
	wait, err := advances.Waiting(ctx, q, docs)
	if err != nil {
		return err
	}
	for i := range list {
		if w := wait[list[i].DocumentID]; w != nil && (list[i].Status == documents.InvReceived || list[i].Status == documents.InvMatched) {
			list[i].AdvanceWait = w
		}
	}
	return nil
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	x, err := Load(r.Context(), s.DB, id)
	if err != nil {
		return err
	}
	if !s.Guard.SeesProperty(r.Context(), u, x.PropertyID) {
		return httpjson.Forbidden("This invoice is outside your access.")
	}
	if s.Held != nil && x.Status == documents.InvPayApproved {
		h, err := s.Held(r.Context(), []int64{x.ID})
		if err != nil {
			return err
		}
		if h[x.ID] != nil {
			x.Held = h[x.ID]
		}
	}
	return httpjson.OK(w, x)
}

// lockInvoice loads an invoice for a state change and checks the user's role and scope at a step.
func lockInvoice(ctx context.Context, tx pgx.Tx, u *access.User, id int64, want, step, right string) (*Invoice, *access.Grant, error) {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM invoice WHERE id=$1 FOR UPDATE`, id).Scan(&status); err != nil {
		return nil, nil, httpjson.NotFound("Invoice")
	}
	x, err := Load(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	if !textclean.Contains(strings.Split(want, ","), status) {
		return nil, nil, httpjson.Conflict("Invoice %s has already moved on.", x.Number)
	}
	g := u.Can(step, right, x.PropertyID, x.ClusterID, nil)
	if g == nil {
		return nil, nil, httpjson.Forbidden("This step belongs to %s.", documents.StepNames[step])
	}
	// Screen levels win over rights: matching needs edit on Invoices, payment on Payments, at the invoice's property.
	if sc := map[string]string{access.Match: "invoices", access.Pay: "payments"}[right]; sc != "" && u.Level(sc, x.PropertyID, x.ClusterID) < screens.Edit {
		return nil, nil, httpjson.Forbidden("Your role can only view %s at %s.", screens.Label(sc), x.Property)
	}
	return x, g, nil
}

func (s *Service) match(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Remark string `json:"remark"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Remark = strings.TrimSpace(in.Remark)
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error { return matchOne(r.Context(), tx, u, id, in.Remark) })
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": "Matched and sent to Finance"})
}

// matchOne confirms the three-way match of one invoice. A difference beyond the tolerance needs a remark.
func matchOne(ctx context.Context, tx pgx.Tx, u *access.User, id int64, remark string) error {
	x, g, err := lockInvoice(ctx, tx, u, id, documents.InvReceived, "ap", access.Match)
	if err != nil {
		return err
	}
	act := "Three-way match confirmed"
	if !x.WithinTol {
		if len(remark) < 5 {
			return httpjson.Bad("Invoice %s is %s %s than the goods received. Open it and write a remark to accept the difference, or send it back.",
				x.Number, money.INR(abs(x.Difference)), map[bool]string{true: "more", false: "less"}[x.Difference > 0])
		}
		act = "Difference accepted"
	}
	if _, err := tx.Exec(ctx, `UPDATE invoice SET status='MATCHED', match_remark=$2, matched_by=$3, matched_at=now() WHERE id=$1`, id, remark, u.ID); err != nil {
		return err
	}
	return auditlog.Doc(ctx, tx, u, g.Name, x.DocumentID, x.DocNumber, "Invoices", act+" · invoice "+x.Number, remark)
}

// sendBack voids the goods receipt and its invoice, and reopens the PO for the store to receive again.
func (s *Service) sendBack(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Remark string `json:"remark"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if len(strings.TrimSpace(in.Remark)) < 5 {
		return httpjson.Bad("Write what the store must check.")
	}
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		x, g, err := lockInvoice(r.Context(), tx, u, id, documents.InvReceived, "ap", access.Match)
		if err != nil {
			return err
		}
		var grnID int64
		if err := tx.QueryRow(r.Context(), `SELECT goods_receipt_id FROM invoice WHERE id=$1`, id).Scan(&grnID); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE document_line l SET received_qty=received_qty-gl.qty FROM goods_receipt_line gl WHERE gl.line_id=l.id AND gl.goods_receipt_id=$1`, grnID); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE goods_receipt SET voided=TRUE, void_reason=$2 WHERE id=$1`, grnID, in.Remark); err != nil {
			return err
		}
		// A vendor's portal invoice it used is free again for the recount.
		if _, err := tx.Exec(r.Context(), `UPDATE vendor_invoice SET goods_receipt_id=NULL WHERE goods_receipt_id=$1`, grnID); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE invoice SET status='VOID', match_remark=$2 WHERE id=$1`, id, in.Remark); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE document SET status='PO_ISSUED', po_closed_short=FALSE, updated_at=now() WHERE id=$1 AND status IN ('PO_ISSUED','RECEIVED')`, x.DocumentID); err != nil {
			return err
		}
		return auditlog.Doc(r.Context(), tx, u, g.Name, x.DocumentID, x.DocNumber, "Invoices", "Sent back to store for recount · "+x.GRN, in.Remark)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": "Sent back to the store"})
}

func (s *Service) approvePayment(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	// The body may list the advances to adjust ({"advances": [{advance_id, amount}]}); without one, the suggestion
	// (this PO's paid advances, then the vendor's carried-forward ones) is adjusted.
	var in struct {
		Advances *[]advances.Pick `json:"advances"`
	}
	if raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20)); len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return httpjson.Bad("Could not read the request.")
		}
	}
	st, err := flowsettings.Load(r.Context(), s.DB)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error { return approveOne(r.Context(), tx, u, st, id, false, in.Advances) })
	if err != nil {
		return err
	}
	msg := "Payment approved. Ready for Zoho."
	if x, err := Load(r.Context(), s.DB, id); err == nil && x.AdvanceUsed > 0 {
		msg += fmt.Sprintf(" Advance of %s adjusted; pay the vendor %s.", money.INR(x.AdvanceUsed-x.AdvanceTDS), money.INR(x.NetPayable))
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

// approveOne approves one matched invoice for payment and sets its pay-by date. It adjusts paid advances: picks, or
// the suggestion when picks is nil (always in bulk).
func approveOne(ctx context.Context, tx pgx.Tx, u *access.User, st flowsettings.Settings, id int64, bulk bool, picks *[]advances.Pick) error {
	zohowake.Now() // something may now go to Zoho
	x, g, err := lockInvoice(ctx, tx, u, id, documents.InvMatched, "finance", access.Pay)
	if err != nil {
		return err
	}
	// An advance on the same PO that Finance has not paid yet must be settled first: once paid it is adjusted here;
	// rejected or cancelled, it no longer holds the invoice. Otherwise the vendor could be paid twice.
	wait, err := advances.Waiting(ctx, tx, []int64{x.DocumentID})
	if err != nil {
		return err
	}
	if w := wait[x.DocumentID]; len(w) > 0 {
		return httpjson.Conflict("Invoice %s waits for the advance on %s: %s. Approve and pay the advance (Payments, Advances tab), or reject it, and then approve this payment.",
			x.Number, x.PONumber, strings.Join(w, "; "))
	}
	adv, err := advances.Apply(ctx, tx, u, id, money.Round2(x.Amount-x.TDS), picks)
	if err != nil {
		return err
	}
	due, _ := payBy(*x, st)
	if _, err := tx.Exec(ctx, `UPDATE invoice SET status='PAY_APPROVED', pay_approved_by=$2, pay_approved_at=now(), due_date=$3 WHERE id=$1`, id, u.ID, due); err != nil {
		return err
	}
	note := "Pay by " + due
	if x.VendorMSME {
		note += " (MSME vendor)"
	}
	if !x.VendorGST {
		note += " · unregistered vendor"
	}
	if adv != "" {
		note += " · " + adv
	}
	if bulk {
		note += " · approved in bulk"
	}
	return auditlog.Doc(ctx, tx, u, g.Name, x.DocumentID, x.DocNumber, "Payments", "Payment approved · invoice "+x.Number, note)
}

type bulkResult struct {
	ID      int64  `json:"id"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// bulk matches (Accounts Payable) or approves for payment (Finance) several invoices, one transaction each.
// A bulk match accepts only invoices within the tolerance; a difference still needs its own remark.
func (s *Service) bulk(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	var in struct {
		Action string  `json:"action"`
		IDs    []int64 `json:"ids"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if in.Action != "match" && in.Action != "approve-payment" {
		return httpjson.Bad("Choose match or approve payment.")
	}
	if len(in.IDs) == 0 {
		return httpjson.Bad("Select at least one invoice.")
	}
	st, err := flowsettings.Load(r.Context(), s.DB)
	if err != nil {
		return err
	}
	res := []bulkResult{}
	done := 0
	for _, id := range in.IDs {
		err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
			if in.Action == "match" {
				return matchOne(r.Context(), tx, u, id, "")
			}
			return approveOne(r.Context(), tx, u, st, id, true, nil)
		})
		if err != nil {
			res = append(res, bulkResult{ID: id, Message: err.Error()})
			continue
		}
		done++
		res = append(res, bulkResult{ID: id, OK: true, Message: map[string]string{"match": "Matched", "approve-payment": "Payment approved"}[in.Action]})
	}
	return httpjson.OK(w, map[string]any{"done": done, "failed": len(in.IDs) - done, "results": res})
}
