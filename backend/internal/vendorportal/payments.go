package vendorportal

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/invoices"
	"purchasedesk/internal/money"
)

type paymentRow struct {
	InvoiceNumber string     `json:"invoice_number"`
	InvoiceDate   string     `json:"invoice_date"`
	PONumber      string     `json:"po_number"`
	Property      string     `json:"property"`
	Amount        float64    `json:"amount"`
	TDS           float64    `json:"tds_amount"`
	Advance       float64    `json:"advance_adjusted"` // advance already paid, set off against this invoice (net of its TDS)
	NetPayable    float64    `json:"net_payable"`
	Stage         string     `json:"stage"`
	PayBy         string     `json:"pay_by"`
	PaidAt        *time.Time `json:"paid_at"`
	PaymentRef    string     `json:"payment_ref"`
}

type advanceRow struct {
	Number     string     `json:"number"`
	PONumber   string     `json:"po_number"`
	Amount     float64    `json:"amount"`
	TDS        float64    `json:"tds_amount"`
	Status     string     `json:"status"`
	PaidOn     *time.Time `json:"paid_on"`
	PaymentRef string     `json:"payment_ref"`
	Adjusted   float64    `json:"adjusted"`
}

// stage says where an invoice is, in the vendor's terms.
func stage(x invoices.Invoice) string {
	switch x.Status {
	case "AWAITING":
		return "Awaiting approval"
	case "RECEIVED":
		return "Received, being checked"
	case "MATCHED":
		return "Checked, awaiting payment approval"
	case "PAY_APPROVED", "PUSHED":
		return "Approved for payment"
	case "PAID":
		return "Paid"
	}
	return x.Status
}

// payments lists the vendor's invoices with where each stands, and its advances.
func (s *Service) payments(w http.ResponseWriter, r *http.Request) error {
	v := from(r)
	list, err := invoices.ForVendor(r.Context(), s.DB, v.ID, false)
	if err != nil {
		return err
	}
	out := struct {
		Invoices []paymentRow `json:"invoices"`
		Advances []advanceRow `json:"advances"`
		Due      float64      `json:"due"`  // approved, not paid yet
		Paid     float64      `json:"paid"` // paid in the last 365 days
	}{Invoices: []paymentRow{}}
	for _, x := range list {
		p := paymentRow{x.Number, x.Date, x.PONumber, x.Property, x.Amount, x.TDS, money.Round2(x.AdvanceUsed - x.AdvanceTDS), x.NetPayable, stage(x), "", x.PaidAt, x.PaymentRef}
		if x.Status == "PAY_APPROVED" || x.Status == "PUSHED" {
			p.PayBy = x.PayBy
			out.Due += x.NetPayable
		}
		if x.Status == "PAID" {
			out.Paid += x.NetPayable
		}
		out.Invoices = append(out.Invoices, p)
	}
	out.Due, out.Paid = money.Round2(out.Due), money.Round2(out.Paid)
	rows, err := s.DB.Query(r.Context(), `SELECT a.number, coalesce(d.po_number,''), a.amount::float8, a.tds_amount::float8,
		CASE a.status WHEN 'PENDING' THEN 'Asked, awaiting approval' WHEN 'APPROVED' THEN 'Approved, to be paid' WHEN 'PAID' THEN 'Paid'
			WHEN 'REJECTED' THEN 'Not approved' ELSE 'Cancelled' END,
		a.paid_on, a.payment_ref, coalesce((SELECT sum(u.amount) FROM advance_use u WHERE u.advance_id=a.id),0)::float8
		FROM advance a JOIN document d ON d.id=a.document_id
		WHERE a.vendor_id=$1 AND a.status IN ('PENDING','APPROVED','PAID') ORDER BY a.requested_at DESC LIMIT 200`, v.ID)
	if err != nil {
		return err
	}
	if out.Advances, err = pgx.CollectRows(rows, pgx.RowToStructByPos[advanceRow]); err != nil {
		return err
	}
	return httpjson.OK(w, out)
}
