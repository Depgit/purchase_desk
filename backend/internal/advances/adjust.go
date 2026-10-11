package advances

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/database"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/money"
)

// Pick is part of a paid advance adjusted against an invoice. Amount is the part of the advance used (including its
// TDS share); the vendor's payment drops by Amount less that TDS share, since the TDS was already held on the advance.
type Pick struct {
	AdvanceID int64   `json:"advance_id"`
	Amount    float64 `json:"amount"`
	// Shown with a suggestion, not read from the client.
	Number    string  `json:"number,omitempty"`
	PONumber  string  `json:"po_number,omitempty"`
	Property  string  `json:"property,omitempty"`
	Open      float64 `json:"open,omitempty"`
	AdvTotal  float64 `json:"advance_amount,omitempty"`
	AdvTDS    float64 `json:"advance_tds,omitempty"`
	SamePO    bool    `json:"same_po,omitempty"`
	Carried   bool    `json:"carried_forward,omitempty"`
	Suggested float64 `json:"suggested,omitempty"`
}

// floor2 rounds down to the paisa, so a suggestion never exceeds what is payable.
func floor2(v float64) float64 { return math.Floor(v*100+1e-6) / 100 }

// tdsShare is the TDS already held on part of an advance.
func tdsShare(advAmount, advTDS, used float64) float64 {
	if advAmount <= 0 || advTDS <= 0 {
		return 0
	}
	return money.Round2(advTDS * used / advAmount)
}

// Plan lists the paid advances an invoice can use, with suggested amounts: this PO's advances first, then the
// vendor's advances carried forward from other POs, oldest first, until payable (invoice amount less TDS) is covered.
func Plan(ctx context.Context, q database.Q, invoiceID int64, payable float64) ([]Pick, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.number, coalesce(d.po_number, d.number), p.name, a.amount::float8, a.tds_amount::float8,
		a.amount::float8 - coalesce((SELECT sum(x.amount) FROM advance_use x WHERE x.advance_id=a.id),0)::float8, a.document_id=i.document_id, a.carry_forward
		FROM invoice i JOIN advance a ON a.status='PAID' AND (a.document_id=i.document_id OR (a.carry_forward AND a.vendor_id=i.vendor_id))
		JOIN document d ON d.id=a.document_id JOIN property p ON p.id=a.property_id
		WHERE i.id=$1 ORDER BY a.document_id=i.document_id DESC, a.paid_on, a.id`, invoiceID)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Pick, error) {
		var p Pick
		err := row.Scan(&p.AdvanceID, &p.Number, &p.PONumber, &p.Property, &p.AdvTotal, &p.AdvTDS, &p.Open, &p.SamePO, &p.Carried)
		p.Open = money.Round2(p.Open)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	out := []Pick{}
	left := payable
	for _, p := range all {
		if p.Open < 0.01 {
			continue
		}
		ratio := 0.0
		if p.AdvTotal > 0 {
			ratio = p.AdvTDS / p.AdvTotal
		}
		use := p.Open
		if left <= 0 {
			use = 0
		} else if ratio < 1 && use-tdsShare(p.AdvTotal, p.AdvTDS, use) > left {
			use = floor2(left / (1 - ratio))
		}
		p.Suggested, p.Amount = use, use
		left -= use - tdsShare(p.AdvTotal, p.AdvTDS, use)
		out = append(out, p)
	}
	return out, nil
}

// Apply adjusts advances against an invoice at payment approval. picks nil takes the Plan's suggestion. Each advance
// must be paid and belong to this PO or be carried forward for this vendor; no advance can go below zero, and the
// vendor's payment cannot go below zero. The caller has locked the invoice. It returns a note for the history.
func Apply(ctx context.Context, tx pgx.Tx, u *access.User, invoiceID int64, payable float64, picks *[]Pick) (string, error) {
	list := []Pick{}
	if picks == nil {
		plan, err := Plan(ctx, tx, invoiceID, payable)
		if err != nil {
			return "", err
		}
		list = plan
	} else {
		list = *picks
	}
	var docID, vendorID int64
	if err := tx.QueryRow(ctx, `SELECT document_id, vendor_id FROM invoice WHERE id=$1`, invoiceID).Scan(&docID, &vendorID); err != nil {
		return "", err
	}
	seen := map[int64]bool{}
	net, notes := 0.0, []string{}
	for _, p := range list {
		p.Amount = money.Round2(p.Amount)
		if p.Amount < 0 {
			return "", httpjson.Bad("An advance adjustment cannot be below zero.")
		}
		if p.Amount == 0 {
			continue
		}
		if seen[p.AdvanceID] {
			return "", httpjson.Bad("An advance is listed twice.")
		}
		seen[p.AdvanceID] = true
		var number, status string
		var advDoc, advVendor int64
		var carry bool
		var amount, tds, used float64
		err := tx.QueryRow(ctx, `SELECT number, status, document_id, vendor_id, carry_forward, amount::float8, tds_amount::float8,
			coalesce((SELECT sum(x.amount) FROM advance_use x WHERE x.advance_id=a.id),0)::float8 FROM advance a WHERE id=$1 FOR UPDATE`, p.AdvanceID).
			Scan(&number, &status, &advDoc, &advVendor, &carry, &amount, &tds, &used)
		if err != nil {
			return "", httpjson.Bad("Unknown advance.")
		}
		if status != "PAID" {
			return "", httpjson.Bad("%s is not paid yet, so it cannot be adjusted.", number)
		}
		if advDoc != docID && !(carry && advVendor == vendorID) {
			return "", httpjson.Bad("%s belongs to another PO. Carry it forward first to use it on this vendor's other invoices.", number)
		}
		open := money.Round2(amount - used)
		if p.Amount > open+0.005 {
			return "", httpjson.Bad("%s has only %s left to adjust.", number, money.INR(open))
		}
		share := tdsShare(amount, tds, p.Amount)
		if _, err := tx.Exec(ctx, `INSERT INTO advance_use(advance_id,kind,invoice_id,amount,tds_amount,created_by) VALUES ($1,'invoice',$2,$3,$4,$5)`,
			p.AdvanceID, invoiceID, p.Amount, share, u.ID); err != nil {
			return "", err
		}
		net += p.Amount - share
		notes = append(notes, fmt.Sprintf("%s %s", number, money.INR(p.Amount)))
	}
	if net > payable+0.01 {
		return "", httpjson.Bad("The advance adjusted (%s) is more than the %s payable on this invoice.", money.INR(net), money.INR(payable))
	}
	if len(notes) == 0 {
		return "", nil
	}
	return "advance adjusted: " + strings.Join(notes, ", "), nil
}

// Release undoes an invoice's advance adjustments, when the invoice goes back for correction (a bill voided in Zoho).
func Release(ctx context.Context, q database.Q, invoiceID int64) error {
	_, err := q.Exec(ctx, `DELETE FROM advance_use WHERE invoice_id=$1 AND kind='invoice'`, invoiceID)
	return err
}

// CancelUnpaid cancels a PO's advances that are not paid yet, once nothing more is due on it (fully received, or the
// balance rejected). A paid advance stays and is adjusted on the invoices, refunded or carried forward.
func CancelUnpaid(ctx context.Context, q database.Q, u *access.User, docID int64, docNumber, why string) error {
	rows, err := q.Query(ctx, `UPDATE advance SET status='CANCELLED', decided_at=now(), decision_note=$2
		WHERE document_id=$1 AND status IN ('PENDING','APPROVED') RETURNING number`, docID, why)
	if err != nil {
		return err
	}
	nums, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, n := range nums {
		if err := auditlog.Doc(ctx, q, u, "System", docID, docNumber, "Payments", "Advance "+n+" cancelled", why); err != nil {
			return err
		}
	}
	return nil
}

// Use is one adjustment or refund of an advance.
type Use struct {
	ID        int64   `json:"id"`
	AdvanceID int64   `json:"advance_id"`
	Advance   string  `json:"advance_number"`
	Kind      string  `json:"kind"`
	InvoiceID *int64  `json:"invoice_id"`
	Invoice   string  `json:"invoice_number"`
	Voucher   string  `json:"voucher_number"`
	Amount    float64 `json:"amount"`
	TDS       float64 `json:"tds_amount"`
	Reference string  `json:"reference"`
	On        string  `json:"used_on"`
	By        string  `json:"by"`
}

const selectUse = `SELECT x.id, x.advance_id, a.number, x.kind, x.invoice_id, coalesce(i.invoice_number,''), coalesce(i.voucher_number,''),
	x.amount::float8, x.tds_amount::float8, x.reference, x.used_on::text, coalesce(u.name,'')
	FROM advance_use x JOIN advance a ON a.id=x.advance_id LEFT JOIN invoice i ON i.id=x.invoice_id LEFT JOIN app_user u ON u.id=x.created_by `

func uses(ctx context.Context, q database.Q, where string, arg int64) ([]Use, error) {
	rows, err := q.Query(ctx, selectUse+where+` ORDER BY x.id`, arg)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Use, error) {
		var x Use
		return x, row.Scan(&x.ID, &x.AdvanceID, &x.Advance, &x.Kind, &x.InvoiceID, &x.Invoice, &x.Voucher, &x.Amount, &x.TDS, &x.Reference, &x.On, &x.By)
	})
}

// UsesForInvoice lists the advances adjusted on one invoice.
func UsesForInvoice(ctx context.Context, q database.Q, invoiceID int64) ([]Use, error) {
	return uses(ctx, q, `WHERE x.invoice_id=$1`, invoiceID)
}
