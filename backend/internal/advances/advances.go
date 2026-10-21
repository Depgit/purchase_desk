// Package advances handles vendor advances against an issued PO: the request by whoever can issue the PO, approval
// and payment by Finance (the same approver as invoice payments), adjustment against the vendor's invoices at payment
// approval, and the refund or carry-forward of what is left once the PO is over.
package advances

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
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
	"purchasedesk/internal/messaging"
	"purchasedesk/internal/money"
	"purchasedesk/internal/screens"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/uploadedfiles"
	"purchasedesk/internal/zohowake"
)

type Service struct {
	DB       *pgxpool.Pool
	Guard    *access.Guard
	Files    *uploadedfiles.Service
	Messages *messaging.Sender
	// POReleased sends the vendor a PO that was raised when its advance was paid (purchaseorders.Release).
	POReleased func(u *access.User, docID int64)
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/advances", httpjson.Wrap(s.list))
	r.Get("/advances/{id}", httpjson.Wrap(s.get))
	r.Get("/documents/{id}/advances", httpjson.Wrap(s.forDocument))
	r.Post("/documents/{id}/advances", httpjson.Wrap(s.request))
	r.Get("/invoices/{id}/advances", httpjson.Wrap(s.forInvoice))
	r.Post("/advances/{id}/approve", httpjson.Wrap(s.decide("approve")))
	r.Post("/advances/{id}/reject", httpjson.Wrap(s.decide("reject")))
	r.Post("/advances/{id}/cancel", httpjson.Wrap(s.decide("cancel")))
	r.Post("/advances/{id}/paid", httpjson.Wrap(s.paid))
	r.Post("/advances/{id}/refund", httpjson.Wrap(s.refund))
	r.Post("/advances/{id}/carry-forward", httpjson.Wrap(s.carry))
}

// Advance statuses.
const (
	Pending   = "PENDING"
	Approved  = "APPROVED"
	Paid      = "PAID"
	Rejected  = "REJECTED"
	Cancelled = "CANCELLED"
)

type Advance struct {
	ID           int64      `json:"id"`
	Number       string     `json:"number"`
	DocumentID   int64      `json:"document_id"`
	DocNumber    string     `json:"document_number"`
	PONumber     string     `json:"po_number"`
	Kind         string     `json:"kind"`
	POOpen       bool       `json:"po_open"` // goods still due on the PO
	POHeld       bool       `json:"po_held"` // the PO waits for this advance to be approved and paid before it is raised
	VendorID     int64      `json:"vendor_id"`
	Vendor       string     `json:"vendor"`
	VendorEmail  string     `json:"-"`
	PropertyID   int64      `json:"property_id"`
	Property     string     `json:"property"`
	ClusterID    int64      `json:"cluster_id"`
	Amount       float64    `json:"amount"`
	Pct          *float64   `json:"pct"`
	TDS          float64    `json:"tds_amount"`
	PayVendor    float64    `json:"pay_vendor"` // amount less TDS
	Reason       string     `json:"reason"`
	Note         string     `json:"note"`
	ProformaID   *int64     `json:"proforma_file_id"`
	Status       string     `json:"status"`
	RequestedBy  int64      `json:"requested_by"`
	Requester    string     `json:"requester"`
	RequestedAt  time.Time  `json:"requested_at"`
	DecidedBy    string     `json:"decided_by"`
	DecidedAt    *time.Time `json:"decided_at"`
	DecisionNote string     `json:"decision_note"`
	PaidOn       string     `json:"paid_on"`
	PaymentRef   string     `json:"payment_ref"`
	CarryForward bool       `json:"carry_forward"`
	Adjusted     float64    `json:"adjusted"`
	Refunded     float64    `json:"refunded"`
	Open         float64    `json:"open"` // paid and not yet adjusted or refunded
	Actions      []string   `json:"actions"`
	Uses         []Use      `json:"uses,omitempty"`
	ZohoPayment  string     `json:"zoho_payment_id"` // the Zoho vendor payment a paid advance became
	ZohoError    string     `json:"zoho_error"`
}

// An opening advance (vendor's opening balance, migration 033) has no PO or property.
const selectAdvance = `SELECT a.id, a.number, coalesce(a.document_id,0), coalesce(d.number,''), coalesce(d.po_number, d.advance_hold_po, CASE WHEN a.opening THEN 'Opening balance' ELSE '' END), coalesce(d.kind,'purchase'),
	coalesce(d.status='PO_ISSUED' OR (d.status='APPROVED' AND d.advance_hold_po IS NOT NULL), FALSE), d.advance_hold_po IS NOT NULL,
	a.vendor_id, v.legal_name, coalesce(v.email::text,''), coalesce(a.property_id,0), coalesce(p.name,'All properties'), coalesce(p.cluster_id,0), a.amount::float8, a.pct::float8, a.tds_amount::float8,
	a.reason, a.note, a.proforma_file_id, a.status, a.requested_by, ru.name, a.requested_at, coalesce(du.name,''), a.decided_at, a.decision_note,
	coalesce(a.paid_on::text,''), a.payment_ref, a.carry_forward,
	coalesce((SELECT sum(x.amount) FROM advance_use x WHERE x.advance_id=a.id AND x.kind='invoice'),0)::float8,
	coalesce((SELECT sum(x.amount) FROM advance_use x WHERE x.advance_id=a.id AND x.kind='refund'),0)::float8, a.zoho_payment_id, a.zoho_error
	FROM advance a LEFT JOIN document d ON d.id=a.document_id JOIN vendor v ON v.id=a.vendor_id LEFT JOIN property p ON p.id=a.property_id
	JOIN app_user ru ON ru.id=a.requested_by LEFT JOIN app_user du ON du.id=a.decided_by `

func scan(row pgx.CollectableRow) (Advance, error) {
	var x Advance
	err := row.Scan(&x.ID, &x.Number, &x.DocumentID, &x.DocNumber, &x.PONumber, &x.Kind, &x.POOpen, &x.POHeld, &x.VendorID, &x.Vendor, &x.VendorEmail,
		&x.PropertyID, &x.Property, &x.ClusterID, &x.Amount, &x.Pct, &x.TDS, &x.Reason, &x.Note, &x.ProformaID, &x.Status, &x.RequestedBy, &x.Requester,
		&x.RequestedAt, &x.DecidedBy, &x.DecidedAt, &x.DecisionNote, &x.PaidOn, &x.PaymentRef, &x.CarryForward, &x.Adjusted, &x.Refunded, &x.ZohoPayment, &x.ZohoError)
	x.PayVendor = money.Round2(x.Amount - x.TDS)
	if x.Status == Paid {
		x.Open = money.Round2(x.Amount - x.Adjusted - x.Refunded)
	}
	x.Actions = []string{}
	return x, err
}

// finance is the approver of the advance: the same Finance role and scope that approves invoice payments, with edit
// on Payments at the advance's property.
func finance(u *access.User, x *Advance) *access.Grant {
	if u.Level("payments", x.PropertyID, x.ClusterID) < screens.Edit {
		return nil
	}
	return u.Can("finance", access.Pay, x.PropertyID, x.ClusterID, nil)
}

// issuer is whoever can issue the PO the advance belongs to; they request and cancel advances.
func issuer(u *access.User, x *Advance) bool {
	if u.Level("purchase_orders", x.PropertyID, x.ClusterID) < screens.Edit {
		return false
	}
	return u.ID == x.RequestedBy || u.Can("purchase", access.IssuePO, x.PropertyID, x.ClusterID, nil) != nil
}

// actions says what the viewer can do with an advance now.
func actions(u *access.User, x *Advance) []string {
	out := []string{}
	fin := finance(u, x) != nil
	switch x.Status {
	case Pending:
		if fin {
			out = append(out, "approve", "reject")
		}
		if issuer(u, x) {
			out = append(out, "cancel")
		}
	case Approved:
		if fin {
			out = append(out, "paid", "reject")
		}
		if issuer(u, x) {
			out = append(out, "cancel")
		}
	case Paid:
		if fin && x.Open > 0.005 {
			out = append(out, "refund")
			if x.CarryForward {
				out = append(out, "keep")
			} else if !x.POOpen {
				out = append(out, "carry")
			}
		}
	}
	return out
}

func (s *Service) load(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, where string, args ...any) ([]Advance, error) {
	rows, err := q.Query(ctx, selectAdvance+where, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scan)
}

// list gives the advances in the viewer's property access: ?status=PENDING,APPROVED and ?open=1 (paid, with a balance).
func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	all, ids, err := s.Guard.Visible(r.Context(), u)
	if err != nil {
		return err
	}
	sts := []string{}
	if v := r.URL.Query().Get("status"); v != "" {
		sts = strings.Split(v, ",")
	}
	out, err := s.load(r.Context(), s.DB, `WHERE ($1 OR a.property_id = ANY($2)) AND (cardinality($3::text[])=0 OR a.status = ANY($3))
		ORDER BY a.requested_at DESC LIMIT 5000`, all, ids, sts)
	if err != nil {
		return err
	}
	open := r.URL.Query().Get("open") == "1"
	shown := []Advance{}
	for _, x := range out {
		if open && x.Open < 0.005 {
			continue
		}
		x.Actions = actions(u, &x)
		shown = append(shown, x)
	}
	return httpjson.OK(w, shown)
}

// one loads an advance the viewer may see.
func (s *Service) one(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, u *access.User, id int64, lock bool) (*Advance, error) {
	where := `WHERE a.id=$1`
	if lock {
		where += ` FOR UPDATE OF a`
	}
	out, err := s.load(ctx, q, where, id)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, httpjson.NotFound("Advance")
	}
	x := &out[0]
	if !s.Guard.SeesProperty(ctx, u, x.PropertyID) {
		return nil, httpjson.Forbidden("This advance is outside your access.")
	}
	x.Actions = actions(u, x)
	return x, nil
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	x, err := s.one(r.Context(), s.DB, u, id, false)
	if err != nil {
		return err
	}
	if x.Uses, err = uses(r.Context(), s.DB, `WHERE x.advance_id=$1`, id); err != nil {
		return err
	}
	return httpjson.OK(w, x)
}

// poValues gives a PO's value including GST and the TDS on its service lines (before GST), at current quantities.
func poValues(ctx context.Context, q pgx.Tx, docID int64) (value, tds float64, err error) {
	err = q.QueryRow(ctx, `SELECT coalesce(sum(qty*rate*(1+gst_pct/100)),0)::float8, coalesce(sum(qty*rate*tds_pct/100),0)::float8 FROM document_line WHERE document_id=$1`, docID).
		Scan(&value, &tds)
	return money.Round2(value), tds, err
}

// requestable checks a PO can take an advance request from u: a purchase (or corporate purchase) on Advance payment
// terms with its PO issued and goods still due, not a warehouse transfer, and u can issue its PO. The first advance
// is asked while issuing the PO; a later one (a second instalment) on the PO page.
func requestable(u *access.User, d *documents.Doc) error {
	if (d.Kind != "purchase" && d.Kind != "corp_purchase") || d.IsTransfer {
		return httpjson.Bad("An advance can be asked only on a vendor's purchase order.")
	}
	if d.PaymentTerms != money.Advance {
		return httpjson.Conflict("%s is on %s terms. An advance is asked only on a PO issued with Advance payment terms.", d.PONumber, d.PaymentTerms)
	}
	held := d.Status == documents.Approved && d.AdvanceHold != ""
	if !held && (d.Status != documents.POIssued || d.PONumber == "") {
		return httpjson.Conflict("An advance can be asked only with the PO, and later while goods are still due on it.")
	}
	if documents.CanAct(u, d, "purchase", access.IssuePO) == nil || u.Level("purchase_orders", d.PropertyID, d.ClusterID) < screens.Edit {
		return httpjson.Forbidden("Only the Purchase Team, who issue this PO, can ask for an advance on it.")
	}
	return nil
}

// forDocument gives a PO's advances and whether the viewer can ask for another.
func (s *Service) forDocument(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	d, err := documents.Load(r.Context(), s.DB, id, false)
	if err != nil {
		return err
	}
	if !s.Guard.SeesProperty(r.Context(), u, d.PropertyID) {
		return httpjson.Forbidden("This document is outside your access.")
	}
	out, err := s.load(r.Context(), s.DB, `WHERE a.document_id=$1 ORDER BY a.id`, id)
	if err != nil {
		return err
	}
	value, asked := 0.0, 0.0
	for _, l := range d.Lines {
		value += money.LineValue(l.Qty, l.Rate, l.GST)
	}
	for i := range out {
		out[i].Actions = actions(u, &out[i])
		if out[i].Status == Pending || out[i].Status == Approved || out[i].Status == Paid {
			asked += out[i].Amount
		}
	}
	var poTDS float64 // TDS on the PO's service lines, so the form can show what the vendor gets
	_ = s.DB.QueryRow(r.Context(), `SELECT coalesce(sum(qty*rate*tds_pct/100),0)::float8 FROM document_line WHERE document_id=$1`, id).Scan(&poTDS)
	paid := 0.0
	for _, x := range out {
		if x.Status == Paid {
			paid += x.Amount
		}
	}
	return httpjson.OK(w, map[string]any{"advances": out, "can_request": d.AdvanceHold == "" && requestable(u, d) == nil, "po_value": money.Round2(value), "po_tds": poTDS,
		"requested": money.Round2(asked), "paid": money.Round2(paid)})
}

// Ask is an advance asked for on a PO: an amount or a % of the PO value, with a reason from the advance_reasons list.
type Ask struct {
	Amount float64 `json:"amount"`
	Pct    float64 `json:"pct"`
	Reason string  `json:"reason"`
	Note   string  `json:"note"`
}

// Check tidies an Ask and checks it on its own, before the PO is touched.
func (in *Ask) Check(ctx context.Context, q database.Q) error {
	in.Note = strings.TrimSpace(in.Note)
	if (in.Amount > 0) == (in.Pct > 0) {
		return httpjson.Bad("Enter the advance as an amount or as a %% of the PO value.")
	}
	if in.Pct > 100 {
		return httpjson.Bad("The advance cannot be more than 100%% of the PO.")
	}
	return flowsettings.CheckReason(ctx, q, "advance_reasons", in.Reason, in.Note)
}

// ReadProforma reads the optional proforma file of a multipart request.
func ReadProforma(r *http.Request) (*uploadedfiles.File, error) {
	if r.MultipartForm == nil {
		return nil, nil
	}
	if fhs := r.MultipartForm.File["proforma"]; len(fhs) > 0 {
		return uploadedfiles.Read(fhs[0])
	}
	return nil, nil
}

// Create records a checked advance on d inside tx, after requestable passes: it is numbered from the PO, kept within
// the PO value, and its TDS is worked out in proportion to the PO's service lines. It returns the message for the user.
func Create(ctx context.Context, tx pgx.Tx, u *access.User, files *uploadedfiles.Service, d *documents.Doc, in Ask, proforma *uploadedfiles.File) (string, error) {
	if err := requestable(u, d); err != nil {
		return "", err
	}
	g := documents.CanAct(u, d, "purchase", access.IssuePO)
	return create(ctx, tx, u, g.Name, files, d, in, proforma)
}

// CreateAtApproval asks the first advance of an Advance-terms PO from the request's final approval (no Purchase Team
// step): the approver is checked by the approval itself, and the PO is already held for Finance under its number.
func CreateAtApproval(ctx context.Context, tx pgx.Tx, u *access.User, role string, d *documents.Doc, in Ask) (string, error) {
	if d.PaymentTerms != money.Advance || d.AdvanceHold == "" {
		return "", httpjson.Conflict("%s is not prepared on Advance terms.", d.Number)
	}
	return create(ctx, tx, u, role, nil, d, in, nil)
}

func create(ctx context.Context, tx pgx.Tx, u *access.User, role string, files *uploadedfiles.Service, d *documents.Doc, in Ask, proforma *uploadedfiles.File) (string, error) {
	po := d.PONumber
	if d.AdvanceHold != "" {
		po = d.AdvanceHold // the PO is raised under this number once the advance is approved and paid
	}
	value, poTDS, err := poValues(ctx, tx, d.ID)
	if err != nil {
		return "", err
	}
	amount := money.Round2(in.Amount)
	var pct *float64
	if in.Pct > 0 {
		amount = money.Round2(value * in.Pct / 100)
		pct = &in.Pct
	}
	var asked float64
	var n int
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(amount) FILTER (WHERE status IN ('PENDING','APPROVED','PAID')),0)::float8, count(*) FROM advance WHERE document_id=$1`, d.ID).
		Scan(&asked, &n); err != nil {
		return "", err
	}
	if amount <= 0 {
		return "", httpjson.Bad("The advance must be more than zero.")
	}
	if asked+amount > value+0.01 {
		return "", httpjson.Bad("Advances on %s would come to %s, more than the PO value of %s. At most %s more can be asked.",
			po, money.INR(asked+amount), money.INR(value), money.INR(math.Max(0, value-asked)))
	}
	tds := 0.0
	if value > 0 && poTDS > 0 {
		tds = math.Round(amount * poTDS / value)
	}
	number := documents.AdvanceNumber(po, n+1)
	var fid *int64
	if proforma != nil && files != nil {
		fileID, err := files.Save(ctx, tx, u, proforma, "proformas", false)
		if err != nil {
			return "", err
		}
		fid = &fileID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO advance(number,document_id,vendor_id,property_id,amount,pct,tds_amount,reason,note,proforma_file_id,status,requested_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'PENDING',$11)`, number, d.ID, d.VendorID, d.PropertyID, amount, pct, tds, in.Reason, in.Note, fid, u.ID); err != nil {
		return "", err
	}
	detail := in.Reason
	if in.Note != "" {
		detail += " · " + in.Note
	}
	if tds > 0 {
		detail += " · TDS " + money.INR(tds)
	}
	msg := fmt.Sprintf("%s for %s sent to Finance for approval", number, money.INR(amount))
	return msg, auditlog.Doc(ctx, tx, u, role, d.ID, d.Number, module(d.Kind), fmt.Sprintf("Advance %s requested · %s", number, money.INR(amount)), detail)
}

// request asks for a further advance on an issued Advance-terms PO (multipart: a data JSON field and an optional
// proforma file). The first advance is asked with the PO itself (purchaseorders).
func (s *Service) request(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return httpjson.Bad("Could not read the form: %v", err)
	}
	var in Ask
	if err := json.Unmarshal([]byte(r.FormValue("data")), &in); err != nil {
		return httpjson.Bad("Could not read the form data.")
	}
	if err := in.Check(r.Context(), s.DB); err != nil {
		return err
	}
	proforma, err := ReadProforma(r)
	if err != nil {
		return err
	}
	var msg string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, err := documents.Load(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		if d.AdvanceHold != "" {
			return httpjson.Conflict("%s is waiting for its first advance to be approved and paid.", d.AdvanceHold)
		}
		msg, err = Create(r.Context(), tx, u, s.Files, d, in, proforma)
		return err
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

func module(kind string) string {
	if kind == "corp_purchase" {
		return "Corporate"
	}
	return "Payments"
}

type noteIn struct {
	Note string `json:"note"`
}

// decide approves (Finance), rejects (Finance, with a reason) or cancels (whoever issues the PO) an unpaid advance.
func (s *Service) decide(action string) httpjson.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		u := access.From(r)
		id, err := httpjson.ID(r, "id")
		if err != nil {
			return err
		}
		var in noteIn
		if err := httpjson.Decode(r, &in); err != nil {
			return err
		}
		in.Note = strings.TrimSpace(in.Note)
		var msg string
		err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
			x, err := s.one(r.Context(), tx, u, id, true)
			if err != nil {
				return err
			}
			if !textclean.Contains(x.Actions, action) {
				switch {
				case x.Status != Pending && x.Status != Approved:
					return httpjson.Conflict("%s has already moved on.", x.Number)
				case action == "approve" && x.Status == Approved:
					return httpjson.Conflict("%s is already approved.", x.Number)
				case action == "cancel":
					return httpjson.Forbidden("Only the Purchase Team, who asked for %s, can cancel it.", x.Number)
				default:
					return httpjson.Forbidden("Advances are approved by %s, as invoice payments are.", documents.StepNames["finance"])
				}
			}
			role := "Purchase Team"
			if g := finance(u, x); g != nil && action != "cancel" {
				role = g.Name
			}
			status, act := Approved, "approved"
			switch action {
			case "approve":
				if !x.POOpen {
					return httpjson.Conflict("Nothing more is due on %s, so %s is no longer needed. Cancel it instead.", x.PONumber, x.Number)
				}
			case "reject":
				if len(in.Note) < 3 {
					return httpjson.Bad("Write why the advance is rejected.")
				}
				status, act = Rejected, "rejected"
			case "cancel":
				status, act = Cancelled, "cancelled"
			}
			if _, err := tx.Exec(r.Context(), `UPDATE advance SET status=$2, decided_by=$3, decided_at=now(), decision_note=$4 WHERE id=$1`, id, status, u.ID, in.Note); err != nil {
				return err
			}
			msg = x.Number + " " + act
			if err := auditlog.Doc(r.Context(), tx, u, role, x.DocumentID, x.DocNumber, module(x.Kind), fmt.Sprintf("Advance %s %s · %s", x.Number, act, money.INR(x.Amount)), in.Note); err != nil {
				return err
			}
			if !x.POHeld {
				return nil
			}
			// The PO was waiting for this advance: approved, it still waits until the advance is paid (releaseHeld);
			// rejected or cancelled, the request goes back to Purchase to issue the PO again.
			if status == Approved {
				msg += ". " + x.PONumber + " goes to " + x.Vendor + " once the advance is paid: mark it paid with the UTR, or read it from Zoho"
				return nil
			}
			po, err := documents.ClearHeldPO(r.Context(), tx, x.DocumentID)
			if err != nil || po == "" {
				return err
			}
			msg += ". " + po + " is not raised: Purchase can issue the PO again"
			return auditlog.Doc(r.Context(), tx, u, role, x.DocumentID, x.DocNumber, module(x.Kind), po+" not raised, advance "+act, in.Note)
		})
		if err != nil {
			return err
		}
		return httpjson.OK(w, map[string]string{"message": msg})
	}
}

// dateOr reads a YYYY-MM-DD date that is not in the future; blank means today.
func dateOr(v string) (time.Time, error) {
	if strings.TrimSpace(v) == "" {
		return clock.Today(), nil
	}
	t, ok := clock.ParseDate(v)
	if !ok {
		return t, httpjson.Bad("Enter the date as YYYY-MM-DD.")
	}
	if t.After(clock.Today()) {
		return t, httpjson.Bad("The date cannot be in the future.")
	}
	return t, nil
}

// paid records that Finance has paid an approved advance (bank reference and date), and tells the vendor.
func (s *Service) paid(w http.ResponseWriter, r *http.Request) error {
	zohowake.Now() // something may now go to Zoho
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Reference string `json:"reference"`
		PaidOn    string `json:"paid_on"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Reference = strings.TrimSpace(in.Reference)
	if len(in.Reference) < 3 {
		return httpjson.Bad("Enter the payment reference (UTR or cheque number).")
	}
	on, err := dateOr(in.PaidOn)
	if err != nil {
		return err
	}
	var x *Advance
	var po string // the held PO raised now the advance is paid
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		if x, err = s.one(r.Context(), tx, u, id, true); err != nil {
			return err
		}
		if !textclean.Contains(x.Actions, "paid") {
			if x.Status != Approved {
				return httpjson.Conflict("%s must be approved before it is paid.", x.Number)
			}
			return httpjson.Forbidden("Advances are paid by %s.", documents.StepNames["finance"])
		}
		if _, err := tx.Exec(r.Context(), `UPDATE advance SET status='PAID', paid_by=$2, paid_on=$3, payment_ref=$4 WHERE id=$1`, id, u.ID, on, in.Reference); err != nil {
			return err
		}
		role := finance(u, x).Name
		if err := auditlog.Doc(r.Context(), tx, u, role, x.DocumentID, x.DocNumber, module(x.Kind),
			fmt.Sprintf("Advance %s paid · %s", x.Number, money.INR(x.PayVendor)), in.Reference+" on "+clock.YMD(on)); err != nil {
			return err
		}
		po, err = releaseHeld(r.Context(), tx, u, role, x)
		return err
	})
	if err != nil {
		return err
	}
	s.tellVendor(u, x, in.Reference, clock.YMD(on))
	msg := fmt.Sprintf("%s paid: %s to %s", x.Number, money.INR(x.PayVendor), x.Vendor)
	if po != "" {
		s.released(u, x.DocumentID)
		msg += ". " + po + " is raised and goes to the vendor"
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

// PaidInZoho marks an approved advance paid from the vendor payment Finance recorded in Zoho (zohopush.ReadAdvances):
// Zoho's date and reference, the payment linked so it is never sent to Zoho again, and the vendor told as usual.
func (s *Service) PaidInZoho(ctx context.Context, id int64, on time.Time, ref, paymentID, paymentNo string) (string, error) {
	var x *Advance
	var po string
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		out, err := s.load(ctx, tx, `WHERE a.id=$1 FOR UPDATE OF a`, id)
		if err != nil {
			return err
		}
		if len(out) == 0 || out[0].Status != Approved {
			return httpjson.Conflict("This advance is no longer waiting for payment.")
		}
		x = &out[0]
		if _, err := tx.Exec(ctx, `UPDATE advance SET status='PAID', paid_on=$2, payment_ref=$3, zoho_payment_id=$4, zoho_error='', zoho_at=now() WHERE id=$1`, id, on, ref, paymentID); err != nil {
			return err
		}
		if err := auditlog.Doc(ctx, tx, nil, "Zoho", x.DocumentID, x.DocNumber, module(x.Kind),
			fmt.Sprintf("Advance %s paid · %s (read from Zoho)", x.Number, money.INR(x.PayVendor)), "Zoho payment "+paymentNo+" · "+ref+" on "+clock.YMD(on)); err != nil {
			return err
		}
		po, err = releaseHeld(ctx, tx, nil, "Zoho", x)
		return err
	})
	if err != nil {
		return "", err
	}
	s.tellVendor(nil, x, ref, clock.YMD(on))
	if po != "" {
		s.released(nil, x.DocumentID)
	}
	return x.Number, nil
}

// releaseHeld raises the PO that waited for this advance, now it is paid (the PO number, or "" when none waited).
func releaseHeld(ctx context.Context, tx pgx.Tx, u *access.User, role string, x *Advance) (string, error) {
	if !x.POHeld {
		return "", nil
	}
	po, err := documents.ReleaseHeldPO(ctx, tx, x.DocumentID)
	if err != nil || po == "" {
		return po, err
	}
	return po, auditlog.Doc(ctx, tx, u, role, x.DocumentID, x.DocNumber, module(x.Kind), po+" issued to vendor", "Advance "+x.Number+" paid")
}

// released hands a PO raised by an advance payment on to the vendor (after the transaction).
func (s *Service) released(u *access.User, docID int64) {
	if s.POReleased != nil {
		s.POReleased(u, docID)
	}
}

// tellVendor emails the vendor that the advance is paid (template advpaid), in the background. u is nil when the
// payment was read from Zoho.
func (s *Service) tellVendor(u *access.User, x *Advance, ref, on string) {
	if s.Messages == nil || x.VendorEmail == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		body, _, err := flowsettings.Template(ctx, s.DB, "advpaid")
		if err != nil {
			return
		}
		text := flowsettings.Fill(body, map[string]string{"vendor": x.Vendor, "amount": money.INR(x.PayVendor), "po_number": x.PONumber, "property": x.Property,
			"date": on, "reference": ref, "advance": x.Number})
		var by *int64
		if u != nil {
			by = &u.ID
		}
		if _, err := s.Messages.Email(ctx, x.VendorEmail, "Advance paid on "+x.PONumber+" · "+x.Property, text, nil, x.PONumber, by); err != nil {
			log.Printf("advance email %s: %v", x.Number, err)
		}
	}()
}

// refund records money the vendor returned from a paid advance (all or part of what is left).
func (s *Service) refund(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Amount    float64 `json:"amount"`
		Reference string  `json:"reference"`
		On        string  `json:"on"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Reference, in.Amount = strings.TrimSpace(in.Reference), money.Round2(in.Amount)
	if len(in.Reference) < 3 {
		return httpjson.Bad("Enter the reference of the vendor's refund (UTR or cheque number).")
	}
	on, err := dateOr(in.On)
	if err != nil {
		return err
	}
	var msg string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		x, err := s.one(r.Context(), tx, u, id, true)
		if err != nil {
			return err
		}
		if !textclean.Contains(x.Actions, "refund") {
			if x.Status != Paid || x.Open < 0.005 {
				return httpjson.Conflict("%s has nothing left to refund.", x.Number)
			}
			return httpjson.Forbidden("Refunds are recorded by %s.", documents.StepNames["finance"])
		}
		if in.Amount <= 0 || in.Amount > x.Open+0.005 {
			return httpjson.Bad("Enter a refund from 0.01 up to the %s left on %s.", money.INR(x.Open), x.Number)
		}
		share := tdsShare(x.Amount, x.TDS, in.Amount)
		if _, err := tx.Exec(r.Context(), `INSERT INTO advance_use(advance_id,kind,amount,tds_amount,reference,used_on,created_by) VALUES ($1,'refund',$2,$3,$4,$5,$6)`,
			id, in.Amount, share, in.Reference, on, u.ID); err != nil {
			return err
		}
		msg = fmt.Sprintf("Refund of %s recorded on %s", money.INR(in.Amount-share), x.Number)
		return auditlog.Doc(r.Context(), tx, u, finance(u, x).Name, x.DocumentID, x.DocNumber, module(x.Kind),
			fmt.Sprintf("Advance %s refunded by vendor · %s", x.Number, money.INR(in.Amount-share)), in.Reference+" on "+clock.YMD(on))
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

// carry marks what is left of a paid advance, on a PO with nothing more due, to be adjusted on the vendor's next
// invoices (carry true), or takes that back (false).
func (s *Service) carry(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Carry bool `json:"carry"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	var msg string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		x, err := s.one(r.Context(), tx, u, id, true)
		if err != nil {
			return err
		}
		want := "carry"
		if !in.Carry {
			want = "keep"
		}
		if !textclean.Contains(x.Actions, want) {
			switch {
			case finance(u, x) == nil:
				return httpjson.Forbidden("Only %s can carry an advance forward.", documents.StepNames["finance"])
			case x.Status != Paid || x.Open < 0.005:
				return httpjson.Conflict("%s has nothing left to carry forward.", x.Number)
			case x.POOpen:
				return httpjson.Conflict("Goods are still due on %s, so %s is adjusted on its own invoices first.", x.PONumber, x.Number)
			default:
				return httpjson.Conflict("%s is already set that way.", x.Number)
			}
		}
		if _, err := tx.Exec(r.Context(), `UPDATE advance SET carry_forward=$2 WHERE id=$1`, id, in.Carry); err != nil {
			return err
		}
		act := fmt.Sprintf("Advance %s carried forward · %s left for %s's next invoices", x.Number, money.INR(x.Open), x.Vendor)
		msg = fmt.Sprintf("%s left on %s will be adjusted on %s's next invoices", money.INR(x.Open), x.Number, x.Vendor)
		if !in.Carry {
			act = "Advance " + x.Number + " no longer carried forward"
			msg = act
		}
		return auditlog.Doc(r.Context(), tx, u, finance(u, x).Name, x.DocumentID, x.DocNumber, module(x.Kind), act, "")
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}

// forInvoice suggests the advances to adjust on a matched invoice at payment approval.
func (s *Service) forInvoice(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var prop int64
	var amount, tds float64
	if err := s.DB.QueryRow(r.Context(), `SELECT i.property_id, i.amount::float8,
		round(coalesce((SELECT sum(gl.qty*l.rate*l.tds_pct/100) FROM goods_receipt_line gl JOIN document_line l ON l.id=gl.line_id WHERE gl.goods_receipt_id=i.goods_receipt_id),0))::float8
		FROM invoice i WHERE i.id=$1`, id).Scan(&prop, &amount, &tds); err != nil {
		return httpjson.NotFound("Invoice")
	}
	if !s.Guard.SeesProperty(r.Context(), u, prop) {
		return httpjson.Forbidden("This invoice is outside your access.")
	}
	plan, err := Plan(r.Context(), s.DB, id, money.Round2(amount-tds))
	if err != nil {
		return err
	}
	used, err := UsesForInvoice(r.Context(), s.DB, id)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"available": plan, "adjusted": used})
}
