// Package httpjson reads JSON requests, writes JSON replies and turns errors into user-facing messages.
package httpjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Error is a user-facing error. Errors lists several validation problems at once.
type Error struct {
	Status int      `json:"-"`
	Msg    string   `json:"error"`
	Errors []string `json:"errors,omitempty"`
}

func (e *Error) Error() string { return e.Msg }

func Bad(format string, a ...any) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Msg: fmt.Sprintf(format, a...)}
}
func Invalid(list []string) *Error {
	msg := "Please fix the errors"
	if len(list) == 1 {
		msg = list[0]
	}
	return &Error{Status: http.StatusUnprocessableEntity, Msg: msg, Errors: list}
}
func Forbidden(format string, a ...any) *Error {
	return &Error{Status: http.StatusForbidden, Msg: fmt.Sprintf(format, a...)}
}
func NotFound(what string) *Error {
	return &Error{Status: http.StatusNotFound, Msg: what + " not found"}
}
func Conflict(format string, a ...any) *Error {
	return &Error{Status: http.StatusConflict, Msg: fmt.Sprintf(format, a...)}
}
func Unauthorized(msg string) *Error { return &Error{Status: http.StatusUnauthorized, Msg: msg} }

type Handler func(w http.ResponseWriter, r *http.Request) error

func Wrap(h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := h(w, r)
		if err == nil {
			return
		}
		var e *Error
		if errors.As(err, &e) {
			JSON(w, e.Status, e)
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			JSON(w, http.StatusNotFound, &Error{Msg: "Not found"})
			return
		}
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			JSON(w, http.StatusConflict, &Error{Msg: "This already exists (" + pe.ConstraintName + ")"})
			return
		}
		log.Printf("ERROR %s %s: %v", r.Method, r.URL.Path, err)
		JSON(w, http.StatusInternalServerError, &Error{Msg: "Something went wrong. Please try again."})
	}
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func OK(w http.ResponseWriter, v any) error {
	JSON(w, http.StatusOK, v)
	return nil
}

func Decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 20<<20))
	if err := dec.Decode(v); err != nil {
		return Bad("Could not read the request: %v", err)
	}
	return nil
}

func ID(r *http.Request, name string) (int64, error) {
	v, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || v <= 0 {
		return 0, Bad("Invalid %s", name)
	}
	return v, nil
}

// QInts reads a query value that may list several ids (property_id=3,5,8); 0 and anything unreadable are left out,
// so an empty list means "all".
func QInts(r *http.Request, name string) []int64 {
	out := []int64{}
	for _, part := range strings.Split(r.URL.Query().Get(name), ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil && n != 0 {
			out = append(out, n)
		}
	}
	return out
}

// QStrings reads a query value that may list several words (module=Purchases,Zoho), sent as repeated values or one
// comma-separated value; blanks are left out, so an empty list means "all".
func QStrings(r *http.Request, name string) []string {
	out := []string{}
	for _, v := range r.URL.Query()[name] {
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func QInt(r *http.Request, name string, def int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return def
	}
	return v
}
