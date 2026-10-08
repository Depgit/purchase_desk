// Package login signs people in with email or mobile and a password or a one-time code,
// and returns the signed-in user with their roles. It also saves each user's screen preferences.
package login

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/messaging"
	"purchasedesk/internal/passwords"
	"purchasedesk/internal/sessiontoken"
)

const sessionTTL = 12 * time.Hour

type Service struct {
	DB       *pgxpool.Pool
	Tokens   sessiontoken.Tokens
	Guard    *access.Guard
	Messages *messaging.Sender
	DevMode  bool

	mu       sync.Mutex
	failures map[string][]time.Time
}

// PublicRoutes need no session.
func (s *Service) PublicRoutes(r chi.Router) {
	r.Post("/auth/login", httpjson.Wrap(s.login))
	r.Post("/auth/otp/request", httpjson.Wrap(s.otpRequest))
	r.Post("/auth/otp/verify", httpjson.Wrap(s.otpVerify))
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/me", httpjson.Wrap(s.me))
	r.Post("/me/password", httpjson.Wrap(s.changePassword))
	r.Get("/prefs/{screen}", httpjson.Wrap(s.getPref))
	r.Put("/prefs/{screen}", httpjson.Wrap(s.putPref))
}

// throttle blocks a login after 8 failures in 15 minutes.
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

func normLogin(l string) string {
	l = strings.TrimSpace(strings.ToLower(l))
	l = strings.TrimPrefix(strings.ReplaceAll(l, " ", ""), "+91")
	return l
}

func (s *Service) findUser(r *http.Request, login string) (id int64, hash *string, mobile, email string, err error) {
	err = s.DB.QueryRow(r.Context(), `SELECT id, password_hash, coalesce(mobile,''), coalesce(email::text,'') FROM app_user WHERE active AND (email=$1::citext OR mobile=$1)`, login).
		Scan(&id, &hash, &mobile, &email)
	return
}

func (s *Service) issue(w http.ResponseWriter, r *http.Request, id int64, how string) error {
	tok, err := s.Tokens.Issue(id, sessionTTL)
	if err != nil {
		return err
	}
	u, err := s.Guard.Load(r.Context(), id)
	if err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Users", "Signed in", u.Name, how)
	return httpjson.OK(w, map[string]any{"token": tok, "user": u})
}

func (s *Service) login(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Login, Password string }
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	key := normLogin(in.Login)
	if s.throttle(key, false) {
		return httpjson.Forbidden("Too many failed attempts. Try again in 15 minutes.")
	}
	id, hash, _, _, err := s.findUser(r, key)
	if err != nil || hash == nil || !passwords.Check(*hash, in.Password) {
		s.throttle(key, true)
		return httpjson.Unauthorized("Email, mobile or password is not correct.")
	}
	return s.issue(w, r, id, "password")
}

func (s *Service) otpRequest(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Login string }
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	key := normLogin(in.Login)
	if s.throttle(key, false) {
		return httpjson.Forbidden("Too many attempts. Try again in 15 minutes.")
	}
	id, _, mobile, email, err := s.findUser(r, key)
	reply := map[string]any{"sent": true}
	if err != nil {
		s.throttle(key, true)
		return httpjson.OK(w, reply) // do not reveal whether the user exists
	}
	code, err := passwords.NewOTP()
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `UPDATE otp_code SET used=TRUE WHERE user_id=$1 AND NOT used`, id); err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `INSERT INTO otp_code(user_id,code_hash,expires_at) VALUES ($1,$2,now()+interval '10 minutes')`, id, passwords.HashOTP(id, code)); err != nil {
		return err
	}
	sent := false
	if key == mobile && s.Messages.SMSOTPReady() {
		sent = s.Messages.SMSOTP(r.Context(), mobile, code) == nil
	} else if email != "" && s.Messages.EmailReady() {
		st, _ := s.Messages.Email(r.Context(), email, "Purchase Desk sign-in code", "Your Purchase Desk sign-in code is "+code+". It is valid for 10 minutes.", nil, "otp", &id)
		sent = st == "sent"
	}
	if !sent {
		if !s.DevMode {
			return httpjson.Bad("One-time codes are not set up yet. Sign in with your password.")
		}
		log.Printf("DEV sign-in code for %s: %s", key, code)
		reply["dev_code"] = code
	}
	return httpjson.OK(w, reply)
}

func (s *Service) otpVerify(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Login, Code string }
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	key := normLogin(in.Login)
	if s.throttle(key, false) {
		return httpjson.Forbidden("Too many attempts. Try again in 15 minutes.")
	}
	id, _, _, _, err := s.findUser(r, key)
	if err != nil {
		s.throttle(key, true)
		return httpjson.Unauthorized("The code is not correct or has expired.")
	}
	var otpID int64
	var hash string
	err = s.DB.QueryRow(r.Context(), `SELECT id, code_hash FROM otp_code WHERE user_id=$1 AND NOT used AND expires_at>now() AND attempts<5 ORDER BY id DESC LIMIT 1`, id).Scan(&otpID, &hash)
	if err != nil || hash != passwords.HashOTP(id, strings.TrimSpace(in.Code)) {
		if err == nil {
			_, _ = s.DB.Exec(r.Context(), `UPDATE otp_code SET attempts=attempts+1 WHERE id=$1`, otpID)
		}
		s.throttle(key, true)
		return httpjson.Unauthorized("The code is not correct or has expired.")
	}
	if _, err := s.DB.Exec(r.Context(), `UPDATE otp_code SET used=TRUE WHERE id=$1`, otpID); err != nil {
		return err
	}
	return s.issue(w, r, id, "one-time code")
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) error {
	return httpjson.OK(w, access.From(r))
}

func (s *Service) changePassword(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	var in struct{ Current, New string }
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	var hash *string
	if err := s.DB.QueryRow(r.Context(), `SELECT password_hash FROM app_user WHERE id=$1`, u.ID).Scan(&hash); err != nil {
		return err
	}
	if hash != nil && !passwords.Check(*hash, in.Current) {
		return httpjson.Bad("Your current password is not correct.")
	}
	if len(in.New) < 8 {
		return httpjson.Bad("The new password must be at least 8 characters.")
	}
	h, err := passwords.Hash(in.New)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `UPDATE app_user SET password_hash=$2 WHERE id=$1`, u.ID, h); err != nil {
		return err
	}
	_ = auditlog.Write(r.Context(), s.DB, u, "Users", "Password changed", u.Name, "")
	return httpjson.OK(w, map[string]bool{"ok": true})
}

func (s *Service) getPref(w http.ResponseWriter, r *http.Request) error {
	var v json.RawMessage
	err := s.DB.QueryRow(r.Context(), `SELECT value FROM user_pref WHERE user_id=$1 AND screen=$2`, access.From(r).ID, chi.URLParam(r, "screen")).Scan(&v)
	if err != nil {
		return httpjson.OK(w, map[string]any{})
	}
	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(v)
	return err
}

func (s *Service) putPref(w http.ResponseWriter, r *http.Request) error {
	var v json.RawMessage
	if err := httpjson.Decode(r, &v); err != nil {
		return err
	}
	if len(v) > 20000 {
		return httpjson.Bad("Preference is too large.")
	}
	_, err := s.DB.Exec(r.Context(), `INSERT INTO user_pref(user_id,screen,value) VALUES ($1,$2,$3) ON CONFLICT (user_id,screen) DO UPDATE SET value=EXCLUDED.value`,
		access.From(r).ID, chi.URLParam(r, "screen"), v)
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]bool{"ok": true})
}
