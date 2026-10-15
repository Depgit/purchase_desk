// Package vendorportal is the vendors' own sign-in. A vendor sees the purchase orders sent to it, accepts them (they
// then await delivery) or declines them, says when it will dispatch, uploads its invoice for the store's goods
// receipt, and follows its payments and advances. One sign-in (an email and its password, vendor_account) can open
// several vendors, for someone who looks after a few small vendors: they pick the vendor after signing in and can
// switch at any time. The Purchase Team gives and resets sign-ins, and can confirm a PO on the vendor's behalf after a
// phone call. A vendor session opens only the vendor portal routes.
package vendorportal

import (
	"context"
	"crypto/rand"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/messaging"
	"purchasedesk/internal/passwords"
	"purchasedesk/internal/sessiontoken"
	"purchasedesk/internal/uploadedfiles"
)

const sessionTTL = 12 * time.Hour

type Service struct {
	DB       *pgxpool.Pool
	Tokens   sessiontoken.Tokens
	Files    *uploadedfiles.Service
	Messages *messaging.Sender
	BaseURL  string // the app's address, for the sign-in link sent to a vendor

	mu       sync.Mutex
	failures map[string][]time.Time
}

// PublicRoutes need no session.
func (s *Service) PublicRoutes(r chi.Router) {
	r.Post("/vendor-portal/login", httpjson.Wrap(s.login))
}

// VendorRoutes are what a signed-in vendor uses.
func (s *Service) VendorRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.middleware)
		r.Get("/vendor-portal/me", httpjson.Wrap(s.me))
		r.Post("/vendor-portal/switch", httpjson.Wrap(s.switchVendor))
		r.Post("/vendor-portal/password", httpjson.Wrap(s.password))
		r.Get("/vendor-portal/orders", httpjson.Wrap(s.orders))
		r.Get("/vendor-portal/orders/{id}", httpjson.Wrap(s.order))
		r.Post("/vendor-portal/orders/{id}/accept", httpjson.Wrap(s.accept))
		r.Post("/vendor-portal/orders/{id}/decline", httpjson.Wrap(s.decline))
		r.Post("/vendor-portal/orders/{id}/dispatch", httpjson.Wrap(s.dispatch))
		r.Post("/vendor-portal/orders/{id}/invoice", httpjson.Wrap(s.invoice))
		r.Get("/vendor-portal/payments", httpjson.Wrap(s.payments))
		r.Get("/vendor-portal/dashboard", httpjson.Wrap(s.dashboard))
		r.Get("/vendor-portal/files/{id}", httpjson.Wrap(s.file))
	})
}

// StaffRoutes are what the Purchase Team uses: the vendor's sign-in, and confirming a PO for the vendor.
func (s *Service) StaffRoutes(r chi.Router) {
	r.Get("/vendors/{id}/portal", httpjson.Wrap(s.access))
	r.Post("/vendors/{id}/portal", httpjson.Wrap(s.give))
	r.Post("/vendors/{id}/portal/off", httpjson.Wrap(s.off))
	r.Post("/documents/{id}/vendor-confirm", httpjson.Wrap(s.confirm))
}

// Vendor is the vendor the signed-in person is working as.
type Vendor struct {
	ID         int64  `json:"id"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Email      string `json:"email"` // the sign-in's email
	MustChange bool   `json:"must_change"`
	AccountID  int64  `json:"-"`
}

// Choice is one vendor a sign-in can open.
type Choice struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// vendorsOf lists the active vendors a sign-in can open, by name.
func (s *Service) vendorsOf(ctx context.Context, accountID int64) ([]Choice, error) {
	rows, err := s.DB.Query(ctx, `SELECT v.id, v.code, v.legal_name FROM vendor_login l JOIN vendor v ON v.id=l.vendor_id
		WHERE l.account_id=$1 AND l.active AND v.active ORDER BY v.legal_name`, accountID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Choice])
}

// loadVendor reads the vendor a sign-in works as, if that sign-in may still open it.
func (s *Service) loadVendor(ctx context.Context, accountID, vendorID int64) (*Vendor, error) {
	v := &Vendor{AccountID: accountID}
	err := s.DB.QueryRow(ctx, `SELECT v.id, v.code, v.legal_name, a.email::text, a.must_change FROM vendor_login l JOIN vendor v ON v.id=l.vendor_id
		JOIN vendor_account a ON a.id=l.account_id WHERE l.vendor_id=$2 AND l.account_id=$1 AND l.active AND v.active`, accountID, vendorID).
		Scan(&v.ID, &v.Code, &v.Name, &v.Email, &v.MustChange)
	return v, err
}

type ctxKey struct{}

func from(r *http.Request) *Vendor { return r.Context().Value(ctxKey{}).(*Vendor) }

// middleware lets a request through only with a vendor session whose sign-in and vendor are both still active.
func (s *Service) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			tok = r.URL.Query().Get("token") // file links opened in a new tab
		}
		aid, vid, err := s.Tokens.ParseVendor(tok)
		if err != nil {
			httpjson.JSON(w, http.StatusUnauthorized, map[string]string{"error": "Please sign in again."})
			return
		}
		v, err := s.loadVendor(r.Context(), aid, vid)
		if err != nil {
			httpjson.JSON(w, http.StatusUnauthorized, map[string]string{"error": "Your sign-in is no longer active. Contact the Saltstayz Purchase Team."})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, v)))
	})
}

// throttle blocks a sign-in after 8 failures in 15 minutes.
func (s *Service) throttle(key string, fail bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures == nil {
		s.failures = map[string][]time.Time{}
	}
	cut := time.Now().Add(-15 * time.Minute)
	kept := []time.Time{}
	for _, t := range s.failures[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if fail {
		kept = append(kept, time.Now())
	}
	s.failures[key] = kept
	return len(kept) >= 8
}

// login checks the email and password and opens the vendor asked for (vendor_id, when the person picked one before),
// else the first of the sign-in's vendors. The reply lists every vendor the sign-in opens, for the picker.
func (s *Service) login(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		VendorID int64  `json:"vendor_id"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	key := strings.ToLower(strings.TrimSpace(in.Email))
	if s.throttle(key, false) {
		return httpjson.Forbidden("Too many failed attempts. Try again in 15 minutes.")
	}
	var aid int64
	var hash string
	err := s.DB.QueryRow(r.Context(), `SELECT id, password_hash FROM vendor_account WHERE email=$1::citext`, key).Scan(&aid, &hash)
	if err != nil || !passwords.Check(hash, in.Password) {
		s.throttle(key, true)
		return httpjson.Unauthorized("Email or password is not correct.")
	}
	list, err := s.vendorsOf(r.Context(), aid)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return httpjson.Forbidden("This sign-in no longer opens any vendor. Contact the Saltstayz Purchase Team.")
	}
	vid := list[0].ID
	for _, c := range list {
		if c.ID == in.VendorID {
			vid = c.ID
		}
	}
	if _, err := s.DB.Exec(r.Context(), `UPDATE vendor_account SET last_login_at=now() WHERE id=$1`, aid); err != nil {
		return err
	}
	return s.session(w, r, aid, vid, list)
}

// session replies with a token for the sign-in working as vendor vid, that vendor, and the vendors it can switch to.
func (s *Service) session(w http.ResponseWriter, r *http.Request, aid, vid int64, list []Choice) error {
	tok, err := s.Tokens.IssueVendor(aid, vid, sessionTTL)
	if err != nil {
		return err
	}
	v, err := s.loadVendor(r.Context(), aid, vid)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"token": tok, "vendor": v, "vendors": list})
}

// me is the vendor being worked as, and every vendor the sign-in can switch to.
func (s *Service) me(w http.ResponseWriter, r *http.Request) error {
	v := from(r)
	list, err := s.vendorsOf(r.Context(), v.AccountID)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"vendor": v, "vendors": list})
}

// switchVendor moves the session to another vendor of the same sign-in.
func (s *Service) switchVendor(w http.ResponseWriter, r *http.Request) error {
	v := from(r)
	var in struct {
		VendorID int64 `json:"vendor_id"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	list, err := s.vendorsOf(r.Context(), v.AccountID)
	if err != nil {
		return err
	}
	for _, c := range list {
		if c.ID == in.VendorID {
			return s.session(w, r, v.AccountID, c.ID, list)
		}
	}
	return httpjson.Forbidden("Your sign-in does not open this vendor.")
}

// password changes the sign-in's password (for every vendor it opens); the one Saltstayz gave must be changed at the
// first sign-in.
func (s *Service) password(w http.ResponseWriter, r *http.Request) error {
	v := from(r)
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	var hash string
	if err := s.DB.QueryRow(r.Context(), `SELECT password_hash FROM vendor_account WHERE id=$1`, v.AccountID).Scan(&hash); err != nil {
		return err
	}
	if !passwords.Check(hash, in.Current) {
		return httpjson.Bad("The current password is not correct.")
	}
	if len(in.New) < 8 || in.New == in.Current {
		return httpjson.Bad("Choose a new password of at least 8 characters.")
	}
	h, err := passwords.Hash(in.New)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `UPDATE vendor_account SET password_hash=$2, must_change=FALSE, updated_at=now() WHERE id=$1`, v.AccountID, h); err != nil {
		return err
	}
	return httpjson.OK(w, map[string]string{"message": "Password changed."})
}

// newPassword is a one-time password for the vendor's first sign-in, without look-alike characters.
func newPassword() (string, error) {
	const set = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		if err != nil {
			return "", err
		}
		b[i] = set[n.Int64()]
	}
	return string(b), nil
}
