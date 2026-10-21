// Package purchaseorders lets the Purchase Team convert an approved request into a purchase order
// (confirming rates, delivery date and payment terms) and sends the PO to the vendor by email.
package purchaseorders

import (
	"context"
	"encoding/json"
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
	"purchasedesk/internal/clock"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/flowsettings"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/messaging"
	"purchasedesk/internal/money"
	"purchasedesk/internal/screens"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/uploadedfiles"
	"purchasedesk/internal/vendorportal"
)

type Service struct {
	DB        *pgxpool.Pool
	Messages  *messaging.Sender
	Files     *uploadedfiles.Service
	PortalURL string // the vendor portal sign-in, named in the PO email to a vendor that has one
}

func (s *Service) Routes(r chi.Router) {
	r.Post("/documents/{id}/po", httpjson.Wrap(s.issue))
	r.Post("/purchase-orders/bulk", httpjson.Wrap(s.bulk))
}

type issueIn struct {
	Rates     map[int64]float64 `json:"rates"`
	DeliverBy string            `json:"deliver_by"`
	Terms     string            `json:"payment_terms"`
	Advance   *advances.Ask     `json:"advance"` // needed with Advance terms: asked of Finance with the PO
}

func (s *Service) issue(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	// JSON, or multipart (a data field plus the advance's proforma) when the PO is issued on Advance terms.
	var in issueIn
	var proforma *uploadedfiles.File
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return httpjson.Bad("Could not read the form: %v", err)
		}
		if err := json.Unmarshal([]byte(r.FormValue("data")), &in); err != nil {
			return httpjson.Bad("Could not read the form data.")
		}
		if proforma, err = advances.ReadProforma(r); err != nil {
			return err
		}
	} else if err := httpjson.Decode(r, &in); err != nil {
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
	if in.Terms != money.Advance {
		in.Advance = nil
	} else if in.Advance == nil {
		return httpjson.Bad("The payment terms are Advance: enter how much advance to ask Finance for.")
	} else if err := in.Advance.Check(r.Context(), s.DB); err != nil {
		return err
	}
	// On Advance terms the PO is prepared and held: it is raised and sent to the vendor only once Finance approves the
	// advance (advances.decide releases it).
	hold := in.Advance != nil
	var po, adv string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		var err error
		if po, err = issueOne(r.Context(), tx, u, id, in.Rates, due, in.Terms, false, hold); err != nil || !hold {
			return err
		}
		d, err := documents.Load(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		adv, err = advances.Create(r.Context(), tx, u, s.Files, d, *in.Advance, proforma)
		return err
	})
	if err != nil {
		return err
	}
	if hold {
		return httpjson.OK(w, map[string]string{"message": adv + ". " + po + " goes to the vendor once Finance approves and pays the advance.", "po_number": ""})
	}
	s.SendPO(u, id)
	return httpjson.OK(w, map[string]string{"message": po + " sent to the vendor", "po_number": po})
}

// issueOne turns one approved request into a PO inside tx. Rates missing from the map keep the request's rate. terms ""
// takes the vendor's own terms. In bulk, a request with estimated rates is refused: those rates need confirming first.
// With hold the PO is prepared (rates, date, terms, its number reserved in advance_hold_po) but not issued: it waits
// for its advance to be approved and paid by Finance.
func issueOne(ctx context.Context, tx pgx.Tx, u *access.User, id int64, rates map[int64]float64, due time.Time, terms string, bulk, hold bool) (string, error) {
	d, err := documents.Load(ctx, tx, id, true)
	if err != nil {
		return "", err
	}
	if d.Status != documents.Approved || (d.Kind != "purchase" && d.Kind != "corp_purchase") {
		return "", httpjson.Conflict("%s is not waiting for a PO.", d.Number)
	}
	if d.AdvanceHold != "" {
		return "", httpjson.Conflict("%s is waiting for its advance. It goes to the vendor once Finance approves and pays the advance; if Finance rejects it, issue the PO again.", d.AdvanceHold)
	}
	if u.Level("purchase_orders", d.PropertyID, d.ClusterID) < screens.Edit {
		return "", httpjson.Forbidden("Your role can only view Purchase Orders at %s, so it cannot issue %s.", d.Property, d.Number)
	}
	g := documents.CanAct(u, d, "purchase", access.IssuePO)
	if g == nil {
		return "", httpjson.Forbidden("Only the Purchase Team can issue a PO.")
	}
	if terms == "" {
		terms = d.VendorTerms
	}
	if !textclean.Contains(money.PaymentTerms, terms) {
		return "", httpjson.Bad("Choose the payment terms for %s. The vendor has none on record.", d.Number)
	}
	if bulk && terms == money.Advance {
		return "", httpjson.Bad("%s is on Advance terms. Issue it on its own page, where the advance is asked with the PO.", d.Number)
	}
	changed := []string{}
	for _, l := range d.Lines {
		if bulk && l.Qty > 0 && l.Estimated {
			return "", httpjson.Bad("%s has an estimated rate for %s. Open it and confirm the rate agreed with the vendor.", d.Number, l.ItemName)
		}
		rate, ok := rates[l.ID]
		if !ok {
			rate = l.Rate
		}
		if l.Qty > 0 && rate <= 0 {
			return "", httpjson.Bad("Enter a rate for %s.", l.ItemName)
		}
		rate = money.Round2(rate)
		if rate != l.Rate {
			changed = append(changed, fmt.Sprintf("%s %.2f to %.2f", l.ItemName, l.Rate, rate))
		}
		if _, err := tx.Exec(ctx, `UPDATE document_line SET rate=$2, estimated=FALSE WHERE id=$1`, l.ID, rate); err != nil {
			return "", err
		}
	}
	po, err := poNumber(ctx, tx, d)
	if err != nil {
		return "", err
	}
	note := terms + ", deliver by " + due.Format("2 Jan")
	if hold {
		if _, err := tx.Exec(ctx, `UPDATE document SET advance_hold_po=$2, deliver_by=$3, payment_terms=$4, updated_at=now() WHERE id=$1`, id, po, due, terms); err != nil {
			return "", err
		}
		if len(changed) > 0 {
			note += ". Rate changed: " + strings.Join(changed, "; ")
		}
		module := "Purchases"
		if d.IsCorp() {
			module = "Corporate"
		}
		return po, auditlog.Doc(ctx, tx, u, g.Name, d.ID, d.Number, module, po+" prepared, waiting for Finance to approve and pay the advance", note)
	}
	if err := raise(ctx, tx, id, po, due, terms); err != nil {
		return "", err
	}
	if len(changed) > 0 {
		note += ". Rate changed: " + strings.Join(changed, "; ")
	}
	if bulk {
		note += " · issued in bulk"
	}
	module := "Purchases"
	if d.IsCorp() {
		module = "Corporate"
	}
	return po, auditlog.Doc(ctx, tx, u, g.Name, d.ID, d.Number, module, po+" issued to vendor", note)
}

// poNumber is the number the request's PO takes: PO_HZ_0001 for PR_HZ_0001, or the next PO- number for older ones.
func poNumber(ctx context.Context, tx pgx.Tx, d *documents.Doc) (string, error) {
	if po, ok := documents.Derived(d.Number, "PO"); ok {
		return po, nil
	}
	return documents.NextNumber(ctx, tx, "po_seq", "PO")
}

// raise issues the PO on an approved request and queues the same PO for Zoho Books.
func raise(ctx context.Context, tx pgx.Tx, id int64, po string, due time.Time, terms string) error {
	if _, err := tx.Exec(ctx, `UPDATE document SET status='PO_ISSUED', po_number=$2, po_date=now(), deliver_by=$3, payment_terms=$4, updated_at=now() WHERE id=$1`,
		id, po, due, terms); err != nil {
		return err
	}
	return documents.QueueZohoPO(ctx, tx, id, "create")
}

// AtApproval is what the final approver gives for the PO their approval raises: the confirmed rate of each line that
// has only an estimated rate (line id → rate), and the advance to ask Finance for when the vendor is on Advance terms.
type AtApproval struct {
	Rates   map[int64]float64 `json:"rates"`
	Advance *advances.Ask     `json:"advance"`
	// Terms are the payment terms the approver chose for this PO (blank: the vendor's own). "Advance" asks the advance.
	Terms string `json:"payment_terms"`
	// DeliverBy is the delivery date the approver gives (YYYY-MM-DD; blank: today + Flow Settings days). Once it has
	// passed (by po_expiry_days) the undelivered balance is rejected (poexpiry).
	DeliverBy string `json:"deliver_by"`
}

// AutoResult is what Auto did: the PO raised (sent to the vendor), or the PO held for Finance with the advance asked.
type AutoResult struct {
	PO, Held, Advance string
}

// Auto raises the PO of a request at its final approval, by the approver as role. With no Purchase Team step (Flow
// Settings, all) every vendor request gets its PO: on the vendor's own terms, at the approved rates (estimated lines at
// the rate the approver confirms), for delivery days after today; on Advance terms the PO is prepared and held, and the
// advance the approver gave goes to Finance. Otherwise only a request whose items' sub-categories all issue POs
// automatically gets one, without estimated rates or Advance terms; the rest wait for the Purchase Team.
func Auto(ctx context.Context, tx pgx.Tx, u *access.User, role string, d *documents.Doc, days int, all bool, a AtApproval) (AutoResult, error) {
	var out AutoResult
	if (d.Kind != "purchase" && d.Kind != "corp_purchase") || d.IsTransfer || d.VendorType == "warehouse" {
		return out, nil
	}
	terms := d.VendorTerms
	if a.Terms != "" {
		if !textclean.Contains(money.PaymentTerms, a.Terms) {
			return out, httpjson.Bad("Choose the payment terms for this PO.")
		}
		terms = a.Terms // the approver's choice for this PO, for example Advance for a vendor normally on credit
	}
	if !textclean.Contains(money.PaymentTerms, terms) {
		if !all {
			return out, nil
		}
		terms = "Net 30" // a vendor without terms on record
	}
	live := []documents.Line{}
	for _, l := range d.Lines {
		if l.Qty > 0 {
			live = append(live, l)
		}
	}
	if len(live) == 0 {
		return out, nil
	}
	if !all {
		if d.Kind != "purchase" || terms == money.Advance {
			return out, nil
		}
		subs := []int64{}
		for _, l := range live {
			if l.Estimated || l.Rate <= 0 {
				return out, nil
			}
			subs = append(subs, l.SubCatID)
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT bool_and(auto_po) FROM sub_category WHERE id = ANY($1)`, subs).Scan(&ok); err != nil || !ok {
			return out, err
		}
	}
	// Estimated rates: the approver confirms each (the PO goes out at that rate).
	errs := []string{}
	confirmed := []string{}
	for _, l := range live {
		if !l.Estimated && l.Rate > 0 {
			continue
		}
		rate := money.Round2(a.Rates[l.ID])
		if rate <= 0 {
			errs = append(errs, fmt.Sprintf("Confirm the rate for %s: it has no rate card, and approving raises the PO at that rate.", l.ItemName))
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE document_line SET rate=$2, estimated=FALSE WHERE id=$1`, l.ID, rate); err != nil {
			return out, err
		}
		confirmed = append(confirmed, fmt.Sprintf("%s %.2f", l.ItemName, rate))
	}
	if terms == money.Advance {
		if a.Advance == nil {
			errs = append(errs, "This PO is on Advance terms: enter the advance to ask Finance for (approving raises the PO, which waits for that advance).")
		} else if err := a.Advance.Check(ctx, tx); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return out, httpjson.Invalid(errs)
	}
	po, err := poNumber(ctx, tx, d)
	if err != nil {
		return out, err
	}
	due := clock.Today().AddDate(0, 0, days)
	if strings.TrimSpace(a.DeliverBy) != "" {
		t, ok := clock.ParseDate(a.DeliverBy)
		if !ok || t.Before(clock.Today()) {
			return out, httpjson.Bad("Choose a delivery date for the PO, today or later.")
		}
		due = t
	}
	note := fmt.Sprintf("%s, deliver by %s", terms, due.Format("2 Jan"))
	if len(confirmed) > 0 {
		note += ". Rate confirmed: " + strings.Join(confirmed, "; ")
	}
	module := "Purchases"
	if d.IsCorp() {
		module = "Corporate"
	}
	if terms == money.Advance {
		// Prepared and held: the PO is raised and sent once the advance is approved and paid (advances.releaseHeld).
		if _, err := tx.Exec(ctx, `UPDATE document SET advance_hold_po=$2, deliver_by=$3, payment_terms=$4, updated_at=now() WHERE id=$1`, d.ID, po, due, terms); err != nil {
			return out, err
		}
		if err := auditlog.Doc(ctx, tx, u, role, d.ID, d.Number, module, po+" prepared at approval, waiting for Finance to approve and pay the advance", note); err != nil {
			return out, err
		}
		held, err := documents.Load(ctx, tx, d.ID, false)
		if err != nil {
			return out, err
		}
		if out.Advance, err = advances.CreateAtApproval(ctx, tx, u, role, held, *a.Advance); err != nil {
			return out, err
		}
		out.Held = po
		return out, nil
	}
	if err := raise(ctx, tx, d.ID, po, due, terms); err != nil {
		return out, err
	}
	out.PO = po
	return out, auditlog.Doc(ctx, tx, u, role, d.ID, d.Number, module, po+" issued to vendor at approval", note)
}

type bulkResult struct {
	ID      int64  `json:"id"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// bulk issues POs for several approved requests at their current rates, one transaction each, and reports per request.
func (s *Service) bulk(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	var in struct {
		IDs       []int64 `json:"ids"`
		DeliverBy string  `json:"deliver_by"`
		Terms     string  `json:"payment_terms"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	if len(in.IDs) == 0 {
		return httpjson.Bad("Select at least one approved request.")
	}
	due, ok := clock.ParseDate(in.DeliverBy)
	if !ok {
		return httpjson.Bad("Choose a delivery date.")
	}
	if due.Before(clock.Today()) {
		return httpjson.Bad("The delivery date cannot be in the past.")
	}
	if in.Terms != "" && !textclean.Contains(money.PaymentTerms, in.Terms) {
		return httpjson.Bad("Choose the payment terms.")
	}
	res := []bulkResult{}
	done := 0
	for _, id := range in.IDs {
		var po string
		err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
			var err error
			po, err = issueOne(r.Context(), tx, u, id, nil, due, in.Terms, true, false)
			return err
		})
		if err != nil {
			res = append(res, bulkResult{ID: id, Message: err.Error()})
			continue
		}
		done++
		s.SendPO(u, id)
		res = append(res, bulkResult{ID: id, OK: true, Message: po + " sent to the vendor"})
	}
	return httpjson.OK(w, map[string]any{"done": done, "failed": len(in.IDs) - done, "results": res})
}

// SendPO emails the PO to the vendor's address from onboarding, using the Admin's template.
func (s *Service) SendPO(u *access.User, docID int64) {
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
		if s.PortalURL != "" && vendorportal.HasLogin(ctx, s.DB, d.VendorID) {
			text += "\n\nPlease accept this order and give its dispatch date on the Saltstayz vendor portal: " + s.PortalURL
		}
		var by *int64 // nil when the PO was raised by an advance payment read from Zoho
		if u != nil {
			by = &u.ID
		}
		if _, err := s.Messages.Email(ctx, d.VendorEmail, "Purchase order "+d.PONumber+" · "+d.Property, text, nil, d.PONumber, by); err != nil {
			log.Printf("PO email %s: %v", d.PONumber, err)
			return
		}
		if d.SentToVendor == nil {
			if err := documents.MarkSentToVendor(ctx, s.DB, d.ID); err == nil {
				_ = auditlog.Doc(ctx, s.DB, u, "", d.ID, d.Number, "Purchases", "Sent to vendor", d.PONumber+" emailed to "+d.VendorEmail)
			}
		}
	}()
}

// Release sends the vendor a PO raised without the Purchase Team (at the final approval, or when its advance is paid;
// u is nil when that payment was read from Zoho). A purchase PO waits as "Push to vendor" and reaches the vendor in its order sheet, sent by the approvers;
// a corporate PO (not in order sheets) is emailed now.
func (s *Service) Release(u *access.User, docID int64) {
	var kind string
	if err := s.DB.QueryRow(context.Background(), `SELECT kind FROM document WHERE id=$1`, docID).Scan(&kind); err == nil && kind == "purchase" {
		return
	}
	s.SendPO(u, docID)
}
