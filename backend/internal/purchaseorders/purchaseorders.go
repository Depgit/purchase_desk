// Package purchaseorders lets the Purchase Team convert an approved request into a purchase order
// (confirming rates, delivery date and payment terms) and sends the PO to the vendor by email.
package purchaseorders

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
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/clock"
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
	r.Post("/documents/{id}/po", httpjson.Wrap(s.issue))
}

type issueIn struct {
	Rates     map[int64]float64 `json:"rates"`
	DeliverBy string            `json:"deliver_by"`
	Terms     string            `json:"payment_terms"`
}

func (s *Service) issue(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in issueIn
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	due, ok := clock.ParseDate(in.DeliverBy)
	if !ok {
		return httpjson.Bad("Choose a delivery date.")
	}
	if due.Before(clock.Today()) {
		return httpjson.Bad("The delivery date cannot be in the past.")
	}
	if !textclean.Contains(money.PaymentTerms, in.Terms) {
		return httpjson.Bad("Choose the payment terms.")
	}
	var po string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, err := documents.Load(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		if d.Status != documents.Approved || (d.Kind != "purchase" && d.Kind != "corp_purchase") {
			return httpjson.Conflict("%s is not waiting for a PO.", d.Number)
		}
		g := documents.CanAct(u, d, "purchase", access.IssuePO)
		if g == nil {
			return httpjson.Forbidden("Only the Purchase Team can issue a PO.")
		}
		changed := []string{}
		for _, l := range d.Lines {
			rate, ok := in.Rates[l.ID]
			if !ok {
				rate = l.Rate
			}
			if l.Qty > 0 && rate <= 0 {
				return httpjson.Bad("Enter a rate for %s.", l.ItemName)
			}
			rate = money.Round2(rate)
			if rate != l.Rate {
				changed = append(changed, fmt.Sprintf("%s %.2f to %.2f", l.ItemName, l.Rate, rate))
			}
			if _, err := tx.Exec(r.Context(), `UPDATE document_line SET rate=$2, estimated=FALSE WHERE id=$1`, l.ID, rate); err != nil {
				return err
			}
		}
		if po, err = documents.NextNumber(r.Context(), tx, "po_seq", "PO"); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE document SET status='PO_ISSUED', po_number=$2, po_date=now(), deliver_by=$3, payment_terms=$4, updated_at=now() WHERE id=$1`,
			id, po, due, in.Terms); err != nil {
			return err
		}
		note := in.Terms + ", deliver by " + due.Format("2 Jan")
		if len(changed) > 0 {
			note += ". Rate changed: " + strings.Join(changed, "; ")
		}
		module := "Purchases"
		if d.IsCorp() {
			module = "Corporate"
		}
		return auditlog.Doc(r.Context(), tx, u, g.Name, d.ID, d.Number, module, po+" issued to vendor", note)
	})
	if err != nil {
		return err
	}
	s.sendPO(u, id)
	return httpjson.OK(w, map[string]string{"message": po + " sent to the vendor", "po_number": po})
}

// sendPO emails the PO to the vendor's address from onboarding, using the Admin's template.
func (s *Service) sendPO(u *access.User, docID int64) {
	if s.Messages == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		d, err := documents.Load(ctx, s.DB, docID, false)
		if err != nil || d.VendorEmail == "" {
			return
		}
		body, _, err := flowsettings.Template(ctx, s.DB, "po")
		if err != nil {
			return
		}
		lines := []string{}
		for _, l := range d.Lines {
			if l.Qty > 0 {
				lines = append(lines, fmt.Sprintf("- %s %s %s (%s): %v × %.2f + GST %v%%", l.SKUCode, l.Brand, l.ItemName, l.PackSize, l.Qty, l.Rate, l.GST))
			}
		}
		text := flowsettings.Fill(body, map[string]string{"vendor": d.Vendor, "po_number": d.PONumber, "property": d.Property, "value": money.INR(d.Total), "due": d.DeliverBy})
		text += "\n\nItems:\n" + strings.Join(lines, "\n")
		if _, err := s.Messages.Email(ctx, d.VendorEmail, "Purchase order "+d.PONumber+" · "+d.Property, text, nil, d.PONumber, &u.ID); err != nil {
			log.Printf("PO email %s: %v", d.PONumber, err)
		}
	}()
}
