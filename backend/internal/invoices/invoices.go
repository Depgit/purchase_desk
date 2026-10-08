// Package invoices runs everything after goods are received: the three-way match by Accounts Payable,
// payment approval by Finance (due date from payment terms, MSME 45-day limit), and the fallback "mark paid".
// One invoice is one payable and becomes one bill in Zoho Books.
package invoices

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
	"purchasedesk/internal/database"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/money"
	"purchasedesk/internal/releasephase"
	"purchasedesk/internal/textclean"
)

type Service struct {
	DB    *pgxpool.Pool
	Guard *access.Guard
	// Held returns, per invoice, what stops it being pushed to Zoho. Set by the Zoho push module.
	Held func(ctx context.Context, ids []int64) (map[int64][]string, error)
	// ZohoConnected reports whether the Zoho connection is live; the manual "mark paid" is a fallback for when it is not.
	ZohoConnected func(ctx context.Context) bool
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/invoices", httpjson.Wrap(s.list))
	r.Get("/invoices/{id}", httpjson.Wrap(s.get))
	r.Post("/invoices/{id}/match", httpjson.Wrap(s.match))
	r.Post("/invoices/{id}/send-back", httpjson.Wrap(s.sendBack))
	r.Post("/invoices/{id}/approve-payment", httpjson.Wrap(s.approvePayment))
	r.Post("/invoices/{id}/mark-paid", httpjson.Wrap(s.markPaid))
}

type Invoice struct {
	ID            int64      `json:"id"`
	DocumentID    int64      `json:"document_id"`
	DocNumber     string     `json:"document_number"`
	Kind          string     `json:"kind"`
	PONumber      string     `json:"po_number"`
	GRN           string     `json:"grn_number"`
	DeliveryNo    int        `json:"delivery_no"`
	ReceivedAt    *time.Time `json:"received_at"`
	VendorID      int64      `json:"vendor_id"`
	Vendor        string     `json:"vendor"`
	VendorMSME    bool       `json:"vendor_msme"`
	VendorGST     bool       `json:"vendor_gst_registered"`
	Terms         string     `json:"payment_terms"`
	PropertyID    int64      `json:"property_id"`
	Property      string     `json:"property"`
	ClusterID     int64      `json:"cluster_id"`
	City          string     `json:"city"`
	BillType      string     `json:"bill_type"`
	Number        string     `json:"invoice_number"`
	Date          string     `json:"invoice_date"`
	Amount        float64    `json:"amount"`
	GST           *float64   `json:"gst_pct"`
	FileID        *int64     `json:"file_id"`
	Status        string     `json:"status"`
	MatchRemark   string     `json:"match_remark"`
	DueDate       string     `json:"due_date"`
	PayBy         string     `json:"pay_by"`
	MSMELimit     string     `json:"msme_limit"`
	ZohoBillID    string     `json:"zoho_bill_id"`
	ZohoStatus    string     `json:"zoho_status"`
	ZohoBalance   *float64   `json:"zoho_balance"`
	ZohoPaid      *float64   `json:"zoho_paid"`
	ZohoError     string     `json:"zoho_error"`
	PaidAt        *time.Time `json:"paid_at"`
	PaymentRef    string     `json:"payment_ref"`
	OrderedValue  float64    `json:"ordered_value"`
	ReceivedValue float64    `json:"received_value"`
	Difference    float64    `json:"difference"`
	WithinTol     bool       `json:"within_tolerance"`
	Held          []string   `json:"held"`
	CreatedAt     time.Time  `json:"created_at"`
	Lines         []RLine    `json:"lines,omitempty"`
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
	i.invoice_number, i.invoice_date::text, i.amount, i.gst_pct, i.file_id, i.status, i.match_remark, coalesce(i.due_date::text,''),
	coalesce(i.zoho_bill_id,''), i.zoho_status, i.zoho_balance, i.zoho_paid, i.zoho_error, i.paid_at, i.payment_ref, i.created_at,
	coalesce((SELECT sum(l.qty*l.rate*(1+l.gst_pct/100)) FROM goods_receipt_line gl JOIN document_line l ON l.id=gl.line_id WHERE gl.goods_receipt_id=g.id),0),
	coalesce((SELECT sum(gl.qty*l.rate*(1+l.gst_pct/100)) FROM goods_receipt_line gl JOIN document_line l ON l.id=gl.line_id WHERE gl.goods_receipt_id=g.id),0)
	FROM invoice i JOIN document d ON d.id=i.document_id JOIN vendor v ON v.id=i.vendor_id JOIN property p ON p.id=i.property_id
	JOIN cluster k ON k.id=p.cluster_id JOIN city c ON c.id=k.city_id
	LEFT JOIN goods_receipt g ON g.id=i.goods_receipt_id LEFT JOIN bill_type bt ON bt.id=d.bill_type_id LEFT JOIN expense_head eh ON eh.id=d.expense_head_id`

func scan(st flowsettings.Settings) func(pgx.CollectableRow) (Invoice, error) {
	return func(row pgx.CollectableRow) (Invoice, error) {
		var x Invoice
		err := row.Scan(&x.ID, &x.DocumentID, &x.DocNumber, &x.Kind, &x.PONumber, &x.GRN, &x.DeliveryNo, &x.ReceivedAt,
			&x.VendorID, &x.Vendor, &x.VendorMSME, &x.VendorGST, &x.Terms, &x.PropertyID, &x.Property, &x.ClusterID, &x.City, &x.BillType,
			&x.Number, &x.Date, &x.Amount, &x.GST, &x.FileID, &x.Status, &x.MatchRemark, &x.DueDate,
			&x.ZohoBillID, &x.ZohoStatus, &x.ZohoBalance, &x.ZohoPaid, &x.ZohoError, &x.PaidAt, &x.PaymentRef, &x.CreatedAt,
			&x.OrderedValue, &x.ReceivedValue)
		if err != nil {
			return x, err
		}
		x.OrderedValue, x.ReceivedValue = money.Round2(x.OrderedValue), money.Round2(x.ReceivedValue)
		bill := x.Kind == "bill" || x.Kind == "corp_bill"
		if bill {
			x.ReceivedValue = x.Amount
		}
		x.Difference = money.Round2(x.Amount - x.ReceivedValue)
		x.WithinTol = bill || abs(x.Difference) <= st.MatchTolPct/100*x.ReceivedValue
		x.PayBy, x.MSMELimit = payBy(x, st)
		x.Held = []string{}
		return x, nil
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// payBy: invoice date plus the payment-term days; an MSME vendor must also be paid within the MSME limit from receipt.
func payBy(x Invoice, st flowsettings.Settings) (string, string) {
	if x.DueDate != "" && !x.VendorMSME {
		return x.DueDate, ""
	}
	base, ok := clock.ParseDate(x.Date)
	if !ok {
		return "", ""
	}
	due := base.AddDate(0, 0, money.TermsDays(x.Terms))
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
	lr, err := q.Query(ctx, `SELECT l.id, coalesce(k.code,''), coalesce(b.name||' '||k.item_name, l.description), coalesce(ps.name, l.unit), coalesce(k.hsn,''), l.qty, gl.qty, l.rate, l.gst_pct
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
	return &x, err
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
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		x, g, err := lockInvoice(r.Context(), tx, u, id, documents.InvReceived, "ap", access.Match)
		if err != nil {
			return err
		}
		act := "Three-way match confirmed"
		if !x.WithinTol {
			if len(in.Remark) < 5 {
				return httpjson.Bad("The invoice is %s %s than the goods received. Write a remark to accept the difference, or send it back.",
					money.INR(abs(x.Difference)), map[bool]string{true: "more", false: "less"}[x.Difference > 0])
			}
			act = "Difference accepted"
		}
		if _, err := tx.Exec(r.Context(), `UPDATE invoice SET status='MATCHED', match_remark=$2, matched_by=$3, matched_at=now() WHERE id=$1`, id, in.Remark, u.ID); err != nil {
			return err
		}
		return auditlog.Doc(r.Context(), tx, u, g.Name, x.DocumentID, x.DocNumber, "Invoices", act+" · invoice "+x.Number, in.Remark)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": "Matched and sent to Finance"})
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
	st, err := flowsettings.Load(r.Context(), s.DB)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		x, g, err := lockInvoice(r.Context(), tx, u, id, documents.InvMatched, "finance", access.Pay)
		if err != nil {
			return err
		}
		due, _ := payBy(*x, st)
		if _, err := tx.Exec(r.Context(), `UPDATE invoice SET status='PAY_APPROVED', pay_approved_by=$2, pay_approved_at=now(), due_date=$3 WHERE id=$1`, id, u.ID, due); err != nil {
			return err
		}
		note := "Pay by " + due
		if x.VendorMSME {
			note += " (MSME vendor)"
		}
		if !x.VendorGST {
			note += " · unregistered vendor"
		}
		return auditlog.Doc(r.Context(), tx, u, g.Name, x.DocumentID, x.DocNumber, "Payments", "Payment approved · invoice "+x.Number, note)
	})
	if err != nil {
		return err
	}
	msg := "Payment approved. Ready for Zoho."
	if !releasephase.On(releasephase.Zoho) {
		msg = "Payment approved."
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

// markPaid is the Admin's fallback, for example while Zoho is disconnected. Normally payment is read back from Zoho.
func (s *Service) markPaid(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedAdmin(u); err != nil {
		return err
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Reference string `json:"reference"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if len(strings.TrimSpace(in.Reference)) < 3 {
		return httpjson.Bad("Enter the payment reference (UTR or cheque number).")
	}
	want := documents.InvPushed
	if !releasephase.On(releasephase.Zoho) || (s.ZohoConnected != nil && !s.ZohoConnected(r.Context())) {
		want += "," + documents.InvPayApproved // without Zoho, payment-approved invoices are closed by hand
	}
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		x, g, err := lockInvoice(r.Context(), tx, u, id, want, "finance", access.Pay)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE invoice SET status='PAID', paid_at=now(), payment_ref=$2 WHERE id=$1`, id, strings.TrimSpace(in.Reference)); err != nil {
			return err
		}
		if err := auditlog.Doc(r.Context(), tx, u, g.Name, x.DocumentID, x.DocNumber, "Payments", "Marked paid by hand · invoice "+x.Number, in.Reference); err != nil {
			return err
		}
		return documents.CloseIfPaid(r.Context(), tx, x.DocumentID)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": "Marked as paid"})
}
