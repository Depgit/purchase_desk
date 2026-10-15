// Package vendorportal is the vendors' own sign-in. A vendor sees the purchase orders sent to it, accepts them (they
// then await delivery) or declines them, says when it will dispatch, uploads its invoice for the store's goods
// receipt, and follows its payments and advances. The Purchase Team gives and resets a vendor's sign-in, and can
// confirm a PO on the vendor's behalf after a phone call. A vendor session opens only the vendor portal routes.
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

// Vendor is the signed-in vendor.
type Vendor struct {
	ID         int64  `json:"id"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	MustChange bool   `json:"must_change"`
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
		id, err := s.Tokens.ParseVendor(tok)
		if err != nil {
			httpjson.JSON(w, http.StatusUnauthorized, map[string]string{"error": "Please sign in again."})
			return
		}
		v := &Vendor{}
		err = s.DB.QueryRow(r.Context(), `SELECT v.id, v.code, v.legal_name, l.email::text, l.must_change FROM vendor_login l JOIN vendor v ON v.id=l.vendor_id
			WHERE l.vendor_id=$1 AND l.active AND v.active`, id).Scan(&v.ID, &v.Code, &v.Name, &v.Email, &v.MustChange)
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

func (s *Service) login(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Email, Password string }
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	key := strings.ToLower(strings.TrimSpace(in.Email))
	if s.throttle(key, false) {
		return httpjson.Forbidden("Too many failed attempts. Try again in 15 minutes.")
	}
	var id int64
	var hash string
	err := s.DB.QueryRow(r.Context(), `SELECT l.vendor_id, l.password_hash FROM vendor_login l JOIN vendor v ON v.id=l.vendor_id
		WHERE l.email=$1::citext AND l.active AND v.active`, key).Scan(&id, &hash)
	if err != nil || !passwords.Check(hash, in.Password) {
		s.throttle(key, true)
		return httpjson.Unauthorized("Email or password is not correct.")
	}
	tok, err := s.Tokens.IssueVendor(id, sessionTTL)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `UPDATE vendor_login SET last_login_at=now() WHERE vendor_id=$1`, id); err != nil {
		return err
	}
	v := &Vendor{}
	if err := s.DB.QueryRow(r.Context(), `SELECT v.id, v.code, v.legal_name, l.email::text, l.must_change FROM vendor_login l JOIN vendor v ON v.id=l.vendor_id WHERE v.id=$1`, id).
		Scan(&v.ID, &v.Code, &v.Name, &v.Email, &v.MustChange); err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"token": tok, "vendor": v})
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) error { return httpjson.OK(w, from(r)) }

// password changes the vendor's password; the one the Purchase Team gave must be changed at the first sign-in.
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
	if err := s.DB.QueryRow(r.Context(), `SELECT password_hash FROM vendor_login WHERE vendor_id=$1`, v.ID).Scan(&hash); err != nil {
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
	if _, err := s.DB.Exec(r.Context(), `UPDATE vendor_login SET password_hash=$2, must_change=FALSE, updated_at=now() WHERE vendor_id=$1`, v.ID, h); err != nil {
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
