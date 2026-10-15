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
// (or the Warehouse Manager, who receives for the warehouse and pre-opening properties) on the POs they receive.
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
	if d.Kind == "purchase" { // the receiving store (or the Warehouse Manager there), never the requester alone
		step := "store"
		if d.Owner() == "wh" {
			step = "wh"
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
	var msg, number, closed string
	var gone []rejected
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, err := documents.Load(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		number = textclean.Default(d.PONumber, d.Number)
		if d.Status != documents.POIssued || d.IsTransfer {
			return httpjson.Conflict("%s has no balance waiting for delivery.", number)
		}
		g := Who(u, d)
		if g == nil {
			return httpjson.Forbidden("Only Purchase, the receiving store or the Admin can reject the balance of %s.", number)
		}
		release := map[int64]float64{}
		left := 0.0
		for _, l := range d.Lines {
			open := l.Qty - l.ReceivedQty
			q := in.Quantities[l.ID]
			if q < 0 {
				return httpjson.Bad("%s: the quantity to reject cannot be below 0.", l.ItemName)
			}
			if q > open {
				return httpjson.Bad("%s: only %v is still due, so you cannot reject %v.", l.ItemName, open, q)
			}
			left += open - q
			if q == 0 {
				continue
			}
			if _, err := tx.Exec(r.Context(), `UPDATE document_line SET qty=qty-$2, rejected_qty=rejected_qty+$2 WHERE id=$1`, l.ID, q); err != nil {
				return err
			}
			if l.SubCatID != 0 {
				release[l.SubCatID] -= money.LineValue(q, l.Rate, l.GST)
			}
			gone = append(gone, rejected{
				text:  fmt.Sprintf("%s %v of %v", l.ItemName, q, l.Qty+l.RejectedQty),
				email: fmt.Sprintf("- %s %s %s (%s): %v", l.SKUCode, l.Brand, l.ItemName, l.PackSize, q),
			})
		}
		if len(gone) == 0 {
			return httpjson.Bad("Enter the quantity to reject for at least one item.")
		}
		if !d.NoBudget() {
			if err := budgets.Commit(r.Context(), tx, d.PropertyID, d.ID, release); err != nil {
				return err
			}
		}
		received := false
		for _, l := range d.Lines {
			received = received || l.ReceivedQty > 0
		}
		items := texts(gone)
		msg = fmt.Sprintf("Balance rejected on %s: %s.", number, items)
		switch {
		case left > 0:
			if _, err := tx.Exec(r.Context(), `UPDATE document SET updated_at=now() WHERE id=$1`, id); err != nil {
				return err
			}
			msg += " The rest stays open for delivery."
		case received:
			// Everything that will come has come: the PO is closed short and moves on with its invoices.
			if _, err := tx.Exec(r.Context(), `UPDATE document SET status='RECEIVED', po_closed_short=TRUE, updated_at=now() WHERE id=$1`, id); err != nil {
				return err
			}
			if err := documents.CloseIfPaid(r.Context(), tx, id); err != nil {
				return err
			}
			msg += " Nothing more is due, so the PO is closed."
			closed = number + " is closed: nothing more is due from the vendor."
		default:
			if _, err := tx.Exec(r.Context(), `UPDATE document SET status='CANCELLED', po_closed_short=TRUE, updated_at=now() WHERE id=$1`, id); err != nil {
				return err
			}
			msg += " Nothing was delivered, so the PO is cancelled."
			closed = number + " is cancelled: nothing was delivered."
		}
		// The Zoho purchase order follows: lower quantities, or cancelled when nothing was delivered.
		zohoAction := "update"
		if left <= 0 && !received {
			zohoAction = "cancel"
		}
		if err := documents.QueueZohoPO(r.Context(), tx, id, zohoAction); err != nil {
			return err
		}
		if left <= 0 {
			// Nothing more is due. With nothing delivered the PO is cancelled, so advances not paid yet are cancelled too;
			// after a part delivery they stay with Finance and the invoices' payment approval waits for them. A paid
			// advance is adjusted on the invoices, refunded or carried forward.
			if !received {
				if err := advances.CancelUnpaid(r.Context(), tx, u, id, d.Number, "Balance rejected on "+number+", nothing delivered"); err != nil {
					return err
				}
			}
			var open float64
			_ = tx.QueryRow(r.Context(), `SELECT coalesce(sum(a.amount - coalesce((SELECT sum(x.amount) FROM advance_use x WHERE x.advance_id=a.id),0)),0)::float8
				FROM advance a WHERE a.document_id=$1 AND a.status='PAID'`, id).Scan(&open)
			if open > 0.005 && !received {
				msg += fmt.Sprintf(" An advance of %s was paid on it: record the vendor's refund or carry it forward under Payments, Advances.", money.INR(open))
			}
		}
		full := in.Reason
		if note != "" {
			full += " · " + note
		}
		return auditlog.Doc(r.Context(), tx, u, g.Name, d.ID, d.Number, "Purchases", "Balance rejected: "+items, full)
	})
	if err != nil {
		return err
	}
	s.notify(u, id, gone, strings.TrimSpace(in.Reason+" "+note))
	// closed tells the form to show that the PO is now closed.
	return httpjson.OK(w, map[string]string{"message": msg, "closed": closed})
}

func texts(rs []rejected) string {
	out := make([]string, len(rs))
	for i, x := range rs {
		out[i] = x.text
	}
	return strings.Join(out, ", ")
}

// notify emails the vendor not to supply the balance and tells the requester on WhatsApp. It runs after the save.
func (s *Service) notify(u *access.User, docID int64, gone []rejected, reason string) {
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
				if _, err := s.Messages.Email(ctx, d.VendorEmail, "Balance cancelled on "+d.PONumber+" · "+d.Property, text, nil, d.PONumber, &u.ID); err != nil {
					log.Printf("balance email %s: %v", d.PONumber, err)
				}
			}
		}
		if d.RequesterMob != "" {
			if body, _, err := flowsettings.Template(ctx, s.DB, "bal"); err == nil {
				text, params := flowsettings.Render(body, map[string]string{"request": d.Number, "property": d.Property, "by": u.Name, "items": texts(gone), "reason": reason})
				if _, err := s.Messages.WhatsApp(ctx, d.RequesterMob, "pd_bal", params, text, d.Number, &u.ID); err != nil {
					log.Printf("balance notify %s: %v", d.Number, err)
				}
			}
		}
	}()
}
