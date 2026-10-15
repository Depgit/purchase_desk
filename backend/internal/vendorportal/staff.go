package vendorportal

import (
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/database"
	"purchasedesk/internal/documents"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/passwords"
)

// URL is the vendor portal's sign-in page.
func (s *Service) URL() string { return strings.TrimRight(s.BaseURL, "/") + "/vendor" }

// HasLogin reports whether a vendor can sign in to the portal, so a PO email can point it there.
func HasLogin(ctx context.Context, q database.Q, vendorID int64) bool {
	var ok bool
	_ = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM vendor_login WHERE vendor_id=$1 AND active)`, vendorID).Scan(&ok)
	return ok
}

// canManage: the vendor portal sign-in is given by whoever edits vendor masters or issues POs.
func canManage(u *access.User) error {
	if u.IsAdmin || u.HasRight(access.Masters) || u.HasRight(access.IssuePO) {
		return nil
	}
	return httpjson.Forbidden("Only the Purchase Team or whoever edits vendors can give a vendor its sign-in.")
}

type accessView struct {
	Enabled     bool       `json:"enabled"`
	Active      bool       `json:"active"`
	Email       string     `json:"email"`
	MustChange  bool       `json:"must_change"`
	LastLoginAt *time.Time `json:"last_login_at"`
	URL         string     `json:"url"`
}

// access says whether a vendor has a portal sign-in.
func (s *Service) access(w http.ResponseWriter, r *http.Request) error {
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	a := accessView{URL: s.URL()}
	err = s.DB.QueryRow(r.Context(), `SELECT active, email::text, must_change, last_login_at FROM vendor_login WHERE vendor_id=$1`, id).Scan(&a.Active, &a.Email, &a.MustChange, &a.LastLoginAt)
	if err == nil {
		a.Enabled = true
	} else if err != pgx.ErrNoRows {
		return err
	} else {
		_ = s.DB.QueryRow(r.Context(), `SELECT email FROM vendor WHERE id=$1`, id).Scan(&a.Email)
	}
	return httpjson.OK(w, a)
}

// give creates the vendor's sign-in, or resets its password: a one-time password the vendor changes at its first
// sign-in. It is shown once here and emailed to the vendor with the sign-in link.
func (s *Service) give(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := canManage(u); err != nil {
		return err
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Email string `json:"email"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	var name, vType, vEmail string
	var active bool
	if err := s.DB.QueryRow(r.Context(), `SELECT legal_name, vendor_type, email, active FROM vendor WHERE id=$1`, id).Scan(&name, &vType, &vEmail, &active); err != nil {
		return httpjson.NotFound("Vendor")
	}
	if !active {
		return httpjson.Bad("%s is inactive. Mark it active first.", name)
	}
	if vType != "goods" {
		return httpjson.Bad("The vendor portal is for goods vendors, who receive purchase orders.")
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" {
		email = strings.ToLower(strings.TrimSpace(vEmail))
	}
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
		return httpjson.Bad("Enter the email address the vendor signs in with.")
	}
	var other string
	_ = s.DB.QueryRow(r.Context(), `SELECT v.legal_name FROM vendor_login l JOIN vendor v ON v.id=l.vendor_id WHERE l.email=$1::citext AND l.vendor_id<>$2`, email, id).Scan(&other)
	if other != "" {
		return httpjson.Bad("%s already signs in with %s. Each vendor needs its own email address.", other, email)
	}
	pw, err := newPassword()
	if err != nil {
		return err
	}
	hash, err := passwords.Hash(pw)
	if err != nil {
		return err
	}
	var reset bool
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		_ = tx.QueryRow(r.Context(), `SELECT true FROM vendor_login WHERE vendor_id=$1`, id).Scan(&reset)
		if _, err := tx.Exec(r.Context(), `INSERT INTO vendor_login(vendor_id,email,password_hash,must_change,active,created_by) VALUES ($1,$2,$3,TRUE,TRUE,$4)
			ON CONFLICT (vendor_id) DO UPDATE SET email=EXCLUDED.email, password_hash=EXCLUDED.password_hash, must_change=TRUE, active=TRUE, updated_at=now()`,
			id, email, hash, u.ID); err != nil {
			return err
		}
		what := "Vendor portal sign-in given"
		if reset {
			what = "Vendor portal password reset"
		}
		return auditlog.Write(r.Context(), tx, u, "Vendors", what, name, email)
	})
	if err != nil {
		return err
	}
	emailed := false
	if s.Messages != nil {
		body := fmt.Sprintf("Dear %s team,\n\nYou can now see and accept Saltstayz purchase orders, give dispatch dates, upload invoices and follow your payments on the Saltstayz vendor portal.\n\nSign in at: %s\nEmail: %s\nOne-time password: %s\n\nYou will be asked to choose your own password the first time you sign in.\n\nRegards,\nSaltstayz Purchase Team",
			name, s.URL(), email, pw)
		status, err := s.Messages.Email(r.Context(), email, "Saltstayz vendor portal sign-in", body, nil, "vendor-portal", &u.ID)
		emailed = err == nil && status != "logged"
	}
	msg := "Sign-in given to " + name
	if reset {
		msg = "Password reset for " + name
	}
	return httpjson.OK(w, map[string]any{"message": msg, "email": email, "password": pw, "url": s.URL(), "emailed": emailed})
}

// off stops a vendor's sign-in.
func (s *Service) off(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := canManage(u); err != nil {
		return err
	}
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var name string
	_ = s.DB.QueryRow(r.Context(), `SELECT legal_name FROM vendor WHERE id=$1`, id).Scan(&name)
	tag, err := s.DB.Exec(r.Context(), `UPDATE vendor_login SET active=FALSE, updated_at=now() WHERE vendor_id=$1 AND active`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpjson.Bad("%s has no active sign-in.", name)
	}
	if err := auditlog.Write(r.Context(), s.DB, u, "Vendors", "Vendor portal sign-in turned off", name, ""); err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": "Sign-in turned off for " + name})
}

// confirm marks a PO accepted for a vendor who confirmed it outside the portal (a phone call or email), or after a
// decline was sorted out with the vendor. The PO then awaits delivery.
func (s *Service) confirm(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		DispatchDate string `json:"dispatch_date"`
		Note         string `json:"note"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	when, err := dispatchDate(in.DispatchDate, false)
	if err != nil {
		return err
	}
	var msg string
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		d, err := documents.Load(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		if d.Status != documents.POIssued || d.IsTransfer || (d.Kind != "purchase" && d.Kind != "corp_purchase") {
			return httpjson.Conflict("%s is not waiting for the vendor.", d.Number)
		}
		if d.VendorStatus == "accepted" {
			return httpjson.Conflict("%s already accepted %s.", d.Vendor, d.PONumber)
		}
		g := documents.POKeeper(u, d)
		if g == nil {
			return httpjson.Forbidden("Only whoever approved %s (or Purchase) can confirm its PO for the vendor.", d.Number)
		}
		if d.VendorStatus == "declined" && len(strings.TrimSpace(in.Note)) < 5 {
			return httpjson.Bad("%s declined this PO. Say what was agreed with the vendor.", d.Vendor)
		}
		note := strings.TrimSpace(in.Note)
		if _, err := tx.Exec(r.Context(), `UPDATE document SET vendor_status='accepted', vendor_acted_at=now(), vendor_note=$2, dispatch_date=coalesce($3, dispatch_date), updated_at=now() WHERE id=$1`,
			d.ID, "Confirmed by "+u.Name+": "+note, when); err != nil {
			return err
		}
		if when != nil {
			note = strings.TrimSpace("Dispatch on " + when.Format("2 Jan 2006") + ". " + note)
		}
		msg = d.PONumber + " confirmed for " + d.Vendor + ", now awaiting delivery"
		return auditlog.Doc(r.Context(), tx, u, g.Name, d.ID, d.Number, "Purchases", "Confirmed with the vendor, awaiting delivery", note)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": msg})
}
