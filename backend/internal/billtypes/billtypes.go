// Package billtypes manages utility and AMC bill types (Electricity, Lift AMC, ...) and their default GST.
package billtypes

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/auditlog"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/indiantax"
	"purchasedesk/internal/textclean"
)

type Service struct{ DB *pgxpool.Pool }

func (s *Service) Routes(r chi.Router) {
	r.Get("/bill-types", httpjson.Wrap(s.list))
	r.Post("/bill-types", httpjson.Wrap(s.save))
	r.Put("/bill-types/{id}", httpjson.Wrap(s.save))
}

type BillType struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Section    string   `json:"section"`
	DefaultGST float64  `json:"default_gst"`
	Active     bool     `json:"active"`
	Vendors    []string `json:"vendors"`
	Usable     int      `json:"usable_vendors"`
	Bills      int      `json:"bills"`
	FinStatus  string   `json:"fin_status"` // pending until Finance approves a new one
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `SELECT b.id, b.name, b.section, b.default_gst, b.active,
		coalesce(array_agg(v.legal_name ORDER BY v.legal_name) FILTER (WHERE v.id IS NOT NULL), '{}'),
		count(v.id) FILTER (WHERE v.active AND v.bank_verified),
		(SELECT count(*) FROM document d WHERE d.bill_type_id=b.id), b.fin_status
		FROM bill_type b LEFT JOIN vendor_bill_type vb ON vb.bill_type_id=b.id LEFT JOIN vendor v ON v.id=vb.vendor_id
		GROUP BY b.id ORDER BY b.section, b.name`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (BillType, error) {
		var x BillType
		return x, row.Scan(&x.ID, &x.Name, &x.Section, &x.DefaultGST, &x.Active, &x.Vendors, &x.Usable, &x.Bills, &x.FinStatus)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

func (s *Service) save(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := access.NeedRight(u, access.Masters, "edit masters"); err != nil {
		return err
	}
	var in struct {
		Name       string  `json:"name"`
		Section    string  `json:"section"`
		DefaultGST float64 `json:"default_gst"`
	}
	if err := httpjson.Decode(r, &in); err != nil {
		return err
	}
	in.Name = textclean.Clean(in.Name)
	if len(in.Name) < 3 {
		return httpjson.Bad("Enter the bill type name.")
	}
	if in.Section != "utility" && in.Section != "amc" {
		return httpjson.Bad("Section must be Utility or AMC.")
	}
	if !indiantax.ValidRate(in.DefaultGST) {
		return httpjson.Bad("GST %% must be 0, 5, 12, 18, 28 or 40.")
	}
	id := int64(0)
	if chi.URLParam(r, "id") != "" {
		var err error
		if id, err = httpjson.ID(r, "id"); err != nil {
			return err
		}
	}
	var dup bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM bill_type WHERE norm_key(name)=norm_key($1) AND id<>$2)`, in.Name, id).Scan(&dup)
	if dup {
		return httpjson.Bad("This bill type already exists.")
	}
	if id == 0 {
		if err := s.DB.QueryRow(r.Context(), `INSERT INTO bill_type(name,section,default_gst,created_by) VALUES ($1,$2,$3,$4) RETURNING id`, in.Name, in.Section, in.DefaultGST, u.ID).Scan(&id); err != nil {
			return err
		}
		_ = auditlog.Write(r.Context(), s.DB, u, "Bills", "Bill type added", in.Name, in.Section)
	} else {
		if _, err := s.DB.Exec(r.Context(), `UPDATE bill_type SET name=$2, section=$3, default_gst=$4 WHERE id=$1`, id, in.Name, in.Section, in.DefaultGST); err != nil {
			return err
		}
		_ = auditlog.Write(r.Context(), s.DB, u, "Bills", "Bill type changed", in.Name, in.Section)
	}
	return httpjson.OK(w, map[string]int64{"id": id})
}
