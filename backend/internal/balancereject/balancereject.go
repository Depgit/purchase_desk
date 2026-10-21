// Package balancereject closes the undelivered balance of a purchase order the vendor will not deliver.
// After a part delivery (8 of 10, "the rest later") the balance stays open on the PO; when it never comes, Purchase,
// the receiving store or the Admin rejects it, line by line. The rejected quantity comes off the line (kept in
// rejected_qty), its value is released from the budget, the vendor and the requester are told, and the PO closes
// once nothing is left open. A new request is raised if the items are still needed.
package balancereject

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/advances"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/budgets"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/messaging"
	"purchasedesk/internal/money"
	"purchasedesk/internal/textclean"
)

type Service struct {
	DB       *pgxpool.Pool
	Messages *messaging.Sender
}

func (s *Service) Routes(r chi.Router) {
	r.Post("/documents/{id}/reject-balance", httpjson.Wrap(s.reject))
}

// Who returns the role a user rejects the balance as, or nil. Purchase and the Admin can on any open PO; the store
// (Warehouse Approver L2 at the warehouse, the Pre-opening Team at a pre-opening property) on the POs they receive.
// The requester cannot, unless they also hold one of those roles.
func Who(u *access.User, d *documents.Doc) *access.Grant {
	if d.Status != documents.POIssued || d.IsTransfer || (d.Kind != "purchase" && d.Kind != "corp_purchase") {
		return nil
	}
	if u.IsAdmin {
		return &access.Grant{Name: "System Admin", Step: "admin"}
	}
	if g := documents.CanAct(u, d, "purchase", access.IssuePO); g != nil {
		return g
	}
	if d.Kind == "purchase" { // the receiving store (Warehouse Approver L2 or Pre-opening Team there), never the requester alone
		step := "store"
		switch d.PropertyKind {
		case "warehouse":
			step = "whl2"
		case "preopening":
			step = "preop"
		}
		return documents.CanAct(u, d, step, access.Receive)
	}
	return nil
}

type rejected struct {
	text  string // "Bath towel 2 of 10"
	email string // a line for the vendor
}

func (s *Service) reject(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Quantities map[int64]float64 `json:"quantities"` // line id → quantity to reject
		Reason     string            `json:"reason"`
		Note       string            `json:"note"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	note := strings.TrimSpace(in.Note)
	if err := flowsettings.CheckReason(r.Context(), s.DB, "balance_reasons", in.Reason, note); err != nil {
		return err
	}
	full := in.Reason
	if note != "" {
		full += " · " + note
	}
	var res Result
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, err := documents.Load(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		number := textclean.Default(d.PONumber, d.Number)
		if d.Status != documents.POIssued || d.IsTransfer {
			return httpjson.Conflict("%s has no balance waiting for delivery.", number)
		}
		g := Who(u, d)
		if g == nil {
			return httpjson.Forbidden("Only Purchase, the receiving store or the Admin can reject the balance of %s.", number)
		}
		res, err = Apply(r.Context(), tx, u, g.Name, d, in.Quantities, full, false)
		return err
	})
	if err != nil {
		return err
	}
	s.Notify(u, id, res, strings.TrimSpace(in.Reason+" "+note))
	// closed tells the form to show that the PO is now closed.
	return httpjson.OK(w, map[string]string{"message": res.Message, "closed": res.Closed})
}

// Result is what a balance rejection did.
type Result struct {
	Message string // for the person (or the log)
	Closed  string // set when nothing more is due: the PO is closed or cancelled
	gone    []rejected
}

// Apply rejects quantities (line id → quantity) of d's undelivered balance inside tx, by u as role (u nil: the
// system, when a PO's delivery date has passed). The rejected quantity comes off the line, the budget is released and
// the PO closes short (or is cancelled when nothing was delivered); the Zoho PO follows. carry: a paid advance with
// something left is carried forward to the vendor's next PO (the automatic rejection does this).
func Apply(ctx context.Context, tx pgx.Tx, u *access.User, role string, d *documents.Doc, quantities map[int64]float64, why string, carry bool) (Result, error) {
	var res Result
	id := d.ID
	number := textclean.Default(d.PONumber, d.Number)
	release := map[int64]float64{}
	left := 0.0
	for _, l := range d.Lines {
		open := l.Qty - l.ReceivedQty
		q := quantities[l.ID]
		if q < 0 {
			return res, httpjson.Bad("%s: the quantity to reject cannot be below 0.", l.ItemName)
		}
		if q > open {
			return res, httpjson.Bad("%s: only %v is still due, so you cannot reject %v.", l.ItemName, open, q)
		}
		left += open - q
		if q == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE document_line SET qty=qty-$2, rejected_qty=rejected_qty+$2 WHERE id=$1`, l.ID, q); err != nil {
			return res, err
		}
		if l.SubCatID != 0 {
			release[l.SubCatID] -= money.LineValue(q, l.Rate, l.GST)
		}
		res.gone = append(res.gone, rejected{
			text:  fmt.Sprintf("%s %v of %v", l.ItemName, q, l.Qty+l.RejectedQty),
			email: fmt.Sprintf("- %s %s %s (%s): %v", l.SKUCode, l.Brand, l.ItemName, l.PackSize, q),
		})
	}
	if len(res.gone) == 0 {
		return res, httpjson.Bad("Enter the quantity to reject for at least one item.")
	}
	if !d.NoBudget() {
		if err := budgets.Commit(ctx, tx, d.PropertyID, d.ID, release); err != nil {
			return res, err
		}
	}
	received := false
	for _, l := range d.Lines {
		received = received || l.ReceivedQty > 0
	}
	items := texts(res.gone)
	res.Message = fmt.Sprintf("Balance rejected on %s: %s.", number, items)
	switch {
	case left > 0:
		if _, err := tx.Exec(ctx, `UPDATE document SET updated_at=now() WHERE id=$1`, id); err != nil {
			return res, err
		}
		res.Message += " The rest stays open for delivery."
	case received:
		// Everything that will come has come: the PO is closed short and moves on with its invoices.
		if _, err := tx.Exec(ctx, `UPDATE document SET status='RECEIVED', po_closed_short=TRUE, updated_at=now() WHERE id=$1`, id); err != nil {
			return res, err
		}
		if err := documents.CloseIfPaid(ctx, tx, id); err != nil {
			return res, err
		}
		res.Message += " Nothing more is due, so the PO is closed."
		res.Closed = number + " is closed: nothing more is due from the vendor."
	default:
		if _, err := tx.Exec(ctx, `UPDATE document SET status='CANCELLED', po_closed_short=TRUE, updated_at=now() WHERE id=$1`, id); err != nil {
			return res, err
		}
		res.Message += " Nothing was delivered, so the PO is cancelled."
		res.Closed = number + " is cancelled: nothing was delivered."
	}
	// The Zoho purchase order follows: lower quantities, or cancelled when nothing was delivered.
	zohoAction := "update"
	if left <= 0 && !received {
		zohoAction = "cancel"
	}
	if err := documents.QueueZohoPO(ctx, tx, id, zohoAction); err != nil {
		return res, err
	}
	if left <= 0 {
		// Nothing more is due. With nothing delivered the PO is cancelled, so advances not paid yet are cancelled too;
		// after a part delivery they stay with Finance and the invoices' payment approval waits for them. A paid
		// advance is adjusted on the invoices, refunded or carried forward.
		if !received {
			if err := advances.CancelUnpaid(ctx, tx, u, id, d.Number, "Balance rejected on "+number+", nothing delivered"); err != nil {
				return res, err
			}
		}
		var open float64
		_ = tx.QueryRow(ctx, `SELECT coalesce(sum(a.amount - coalesce((SELECT sum(x.amount) FROM advance_use x WHERE x.advance_id=a.id),0)),0)::float8
			FROM advance a WHERE a.document_id=$1 AND a.status='PAID'`, id).Scan(&open)
		switch {
		case open > 0.005 && carry:
			// Carried forward: the vendor's next PO shows it and its invoice takes it at payment approval (after any
			// invoice of this PO, which comes first).
			if _, err := tx.Exec(ctx, `UPDATE advance SET carry_forward=TRUE WHERE document_id=$1 AND status='PAID'`, id); err != nil {
				return res, err
			}
			res.Message += fmt.Sprintf(" The advance of %s paid on it is carried forward to %s's next PO.", money.INR(open), d.Vendor)
			if err := auditlog.Doc(ctx, tx, u, role, d.ID, d.Number, "Payments", "Advance carried forward to the vendor's next PO", money.INR(open)); err != nil {
				return res, err
			}
		case open > 0.005 && !received:
			res.Message += fmt.Sprintf(" An advance of %s was paid on it: record the vendor's refund or carry it forward under Payments, Advances.", money.INR(open))
		}
	}
	return res, auditlog.Doc(ctx, tx, u, role, d.ID, d.Number, "Purchases", "Balance rejected: "+items, why)
}

func texts(rs []rejected) string {
	out := make([]string, len(rs))
	for i, x := range rs {
		out[i] = x.text
	}
	return strings.Join(out, ", ")
}

// Notify emails the vendor not to supply the balance and tells the requester on WhatsApp. It runs after the save; u
// is nil for the automatic rejection.
func (s *Service) Notify(u *access.User, docID int64, res Result, reason string) {
	gone := res.gone
	var by *int64
	byName := "Purchase Desk (delivery date passed)"
	if u != nil {
		by, byName = &u.ID, u.Name
	}
	if s.Messages == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		d, err := documents.Load(ctx, s.DB, docID, false)
		if err != nil {
			return
		}
		if d.VendorEmail != "" && d.PONumber != "" {
			if body, _, err := flowsettings.Template(ctx, s.DB, "pobal"); err == nil {
				lines := make([]string, len(gone))
				for i, x := range gone {
					lines[i] = x.email
				}
				text := flowsettings.Fill(body, map[string]string{"vendor": d.Vendor, "po_number": d.PONumber, "property": d.Property, "items": strings.Join(lines, "\n"), "reason": reason})
				if _, err := s.Messages.Email(ctx, d.VendorEmail, "Balance cancelled on "+d.PONumber+" · "+d.Property, text, nil, d.PONumber, by); err != nil {
					log.Printf("balance email %s: %v", d.PONumber, err)
				}
			}
		}
		if d.RequesterMob != "" {
			if body, _, err := flowsettings.Template(ctx, s.DB, "bal"); err == nil {
				text, params := flowsettings.Render(body, map[string]string{"request": d.Number, "property": d.Property, "by": byName, "items": texts(gone), "reason": reason})
				if _, err := s.Messages.WhatsApp(ctx, d.RequesterMob, "pd_bal", params, text, d.Number, by); err != nil {
					log.Printf("balance notify %s: %v", d.Number, err)
				}
			}
		}
	}()
}
