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
	if u.IsAdmin || u.HasRight(access.Masters) || u.HasRight(access.Catalog) || u.HasRight(access.IssuePO) {
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
	Others      []string   `json:"others"` // the other vendors the same sign-in opens
}

// access says whether a vendor has a portal sign-in, and which other vendors that sign-in opens.
func (s *Service) access(w http.ResponseWriter, r *http.Request) error {
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	a := accessView{URL: s.URL(), Others: []string{}}
	var aid int64
	err = s.DB.QueryRow(r.Context(), `SELECT l.active, a.email::text, a.must_change, a.last_login_at, a.id FROM vendor_login l JOIN vendor_account a ON a.id=l.account_id
		WHERE l.vendor_id=$1`, id).Scan(&a.Active, &a.Email, &a.MustChange, &a.LastLoginAt, &aid)
	switch {
	case err == nil:
		a.Enabled = true
		rows, err := s.DB.Query(r.Context(), `SELECT v.legal_name FROM vendor_login l JOIN vendor v ON v.id=l.vendor_id
			WHERE l.account_id=$1 AND l.vendor_id<>$2 AND l.active AND v.active ORDER BY 1`, aid, id)
		if err != nil {
			return err
		}
		if a.Others, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
	case err == pgx.ErrNoRows:
		_ = s.DB.QueryRow(r.Context(), `SELECT email FROM vendor WHERE id=$1`, id).Scan(&a.Email)
	default:
		return err
	}
	return httpjson.OK(w, a)
}

// give links the vendor to the portal sign-in of an email. One email can open several vendors (someone who looks
// after a few small vendors), so:
//   - a new email gets a sign-in with a one-time password, shown once here and emailed;
//   - an email that already signs in keeps its password: the vendor is added to what it opens;
//   - the vendor's own email again, while it is on, resets that sign-in's password (for every vendor it opens);
//   - a vendor that was turned off is turned on again.
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
	var pw, msg, what string
	var others int
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		var aid, curAid int64
		var linkActive bool
		_ = tx.QueryRow(r.Context(), `SELECT id FROM vendor_account WHERE email=$1::citext FOR UPDATE`, email).Scan(&aid)
		_ = tx.QueryRow(r.Context(), `SELECT account_id, active FROM vendor_login WHERE vendor_id=$1`, id).Scan(&curAid, &linkActive)
		switch {
		case aid == 0: // a new sign-in
			if pw, err = newPassword(); err != nil {
				return err
			}
			hash, err := passwords.Hash(pw)
			if err != nil {
				return err
			}
			if err := tx.QueryRow(r.Context(), `INSERT INTO vendor_account(email,password_hash,must_change,created_by) VALUES ($1,$2,TRUE,$3) RETURNING id`, email, hash, u.ID).Scan(&aid); err != nil {
				return err
			}
			what, msg = "Vendor portal sign-in given", "Sign-in given to "+name
		case aid == curAid && linkActive: // the same sign-in again: a new one-time password
			if pw, err = newPassword(); err != nil {
				return err
			}
			hash, err := passwords.Hash(pw)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), `UPDATE vendor_account SET password_hash=$2, must_change=TRUE, updated_at=now() WHERE id=$1`, aid, hash); err != nil {
				return err
			}
			what, msg = "Vendor portal password reset", "Password reset for "+email
		case aid == curAid:
			what, msg = "Vendor portal sign-in turned on again", "Sign-in turned on again for "+name+". "+email+" keeps its password."
		default: // an email that already signs in for other vendors: add this one
			what, msg = "Vendor added to a portal sign-in", name+" added to the sign-in of "+email+". They sign in with their current password and pick "+name+"."
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO vendor_login(vendor_id,account_id,active,created_by) VALUES ($1,$2,TRUE,$3)
			ON CONFLICT (vendor_id) DO UPDATE SET account_id=EXCLUDED.account_id, active=TRUE, updated_at=now()`, id, aid, u.ID); err != nil {
			return err
		}
		_ = tx.QueryRow(r.Context(), `SELECT count(*) FROM vendor_login WHERE account_id=$1 AND vendor_id<>$2 AND active`, aid, id).Scan(&others)
		return auditlog.Write(r.Context(), tx, u, "Vendors", what, name, email)
	})
	if err != nil {
		return err
	}
	if pw != "" && others > 0 {
		msg += fmt.Sprintf(". The new password also applies to the %d other vendor(s) this email opens", others)
	}
	emailed := false
	if s.Messages != nil {
		var body string
		if pw != "" {
			body = fmt.Sprintf("Dear %s team,\n\nYou can now see and accept Saltstayz purchase orders, give dispatch dates, upload invoices and follow your payments on the Saltstayz vendor portal.\n\nSign in at: %s\nEmail: %s\nOne-time password: %s\n\nYou will be asked to choose your own password the first time you sign in.\n\nRegards,\nSaltstayz Purchase Team",
				name, s.URL(), email, pw)
		} else {
			body = fmt.Sprintf("Dear %s team,\n\n%s can now be opened on the Saltstayz vendor portal with your existing sign-in (%s). After signing in at %s, pick %s from the vendor list.\n\nRegards,\nSaltstayz Purchase Team",
				name, name, email, s.URL(), name)
		}
		status, err := s.Messages.Email(r.Context(), email, "Saltstayz vendor portal sign-in", body, nil, "vendor-portal", &u.ID)
		emailed = err == nil && status != "logged"
	}
	return httpjson.OK(w, map[string]any{"message": msg, "email": email, "password": pw, "url": s.URL(), "emailed": emailed})
}

// off stops a vendor's sign-in. The same email keeps opening its other vendors.
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
