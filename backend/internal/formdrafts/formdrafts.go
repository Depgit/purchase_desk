// Package formdrafts keeps half-filled forms (purchase request, goods receipt invoice, utility/AMC bill, corporate purchase or
// bill) so their author can finish them later, on any device. A draft is private to the person who saved it. Its
// fields are kept as the browser sent them and checked only when the form is finally submitted; attached files are
// stored like any other upload and come back with the draft.
package formdrafts

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/httpjson"
	"purchasedesk/internal/textclean"
	"purchasedesk/internal/uploadedfiles"
)

// Forms that may be saved as drafts.
var forms = map[string]bool{"grn": true, "bill": true, "corporate": true, "request": true}

const (
	maxFields = 200_000 // bytes of form fields
	maxDrafts = 30      // per person and form
	maxFiles  = 12      // per draft
)

type Service struct {
	DB    *pgxpool.Pool
	Files *uploadedfiles.Service
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/drafts", httpjson.Wrap(s.list))
	r.Post("/drafts", httpjson.Wrap(s.save))
	r.Delete("/drafts/{id}", httpjson.Wrap(s.remove))
}

// File is one attached file kept with a draft, under the form field it belongs to (invoice, photos, quote).
type File struct {
	Field string `json:"field"`
	ID    int64  `json:"id"`
	Name  string `json:"name"`
}

type Draft struct {
	ID        int64           `json:"id"`
	Form      string          `json:"form"`
	RefID     *int64          `json:"ref_id"`
	Title     string          `json:"title"`
	Fields    json.RawMessage `json:"fields"`
	Files     []File          `json:"files"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// list gives the signed-in person's drafts of one form, newest first; ?ref= narrows a goods receipt draft to its PO.
func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	form := r.URL.Query().Get("form")
	if !forms[form] {
		return httpjson.Bad("Unknown form.")
	}
	ref, _ := strconv.ParseInt(r.URL.Query().Get("ref"), 10, 64)
	rows, err := s.DB.Query(r.Context(), `SELECT id, form, ref_id, title, fields, files, updated_at FROM form_draft
		WHERE user_id=$1 AND form=$2 AND ($3=0 OR ref_id=$3) ORDER BY updated_at DESC`, u.ID, form, ref)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Draft, error) {
		var d Draft
		var files []byte
		if err := row.Scan(&d.ID, &d.Form, &d.RefID, &d.Title, &d.Fields, &files, &d.UpdatedAt); err != nil {
			return d, err
		}
		d.Files = []File{}
		return d, json.Unmarshal(files, &d.Files)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, out)
}

// save adds a draft, or replaces one the person saved before (id). It is multipart: a data field with
// {id, form, ref_id, title, fields, keep}, where keep lists files already on the draft that stay, plus new files,
// each under its form field name.
func (s *Service) save(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	if err := r.ParseMultipartForm(4*uploadedfiles.MaxBytes + (1 << 20)); err != nil {
		return httpjson.Bad("The draft could not be read. Try again.")
	}
	var in struct {
		ID     int64           `json:"id"`
		Form   string          `json:"form"`
		RefID  *int64          `json:"ref_id"`
		Title  string          `json:"title"`
		Fields json.RawMessage `json:"fields"`
		Keep   []int64         `json:"keep"`
	}
	if err := json.Unmarshal([]byte(r.FormValue("data")), &in); err != nil {
		return httpjson.Bad("The draft could not be read. Try again.")
	}
	if !forms[in.Form] {
		return httpjson.Bad("Unknown form.")
	}
	if len(in.Fields) == 0 || !json.Valid(in.Fields) {
		in.Fields = json.RawMessage("{}")
	}
	if len(in.Fields) > maxFields {
		return httpjson.Bad("This draft is too large to save.")
	}
	in.Title = textclean.Default(textclean.Clean(in.Title), "Draft")
	if len(in.Title) > 200 {
		in.Title = in.Title[:200]
	}
	news := []*uploadedfiles.File{}
	newsField := []string{}
	if r.MultipartForm != nil {
		for field, fhs := range r.MultipartForm.File {
			for _, fh := range fhs {
				f, err := uploadedfiles.Read(fh)
				if err != nil {
					return err
				}
				news, newsField = append(news, f), append(newsField, field)
			}
		}
	}
	var id int64
	files := []File{}
	err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		if in.ID != 0 {
			var raw []byte
			if err := tx.QueryRow(r.Context(), `SELECT files FROM form_draft WHERE id=$1 AND user_id=$2 FOR UPDATE`, in.ID, u.ID).Scan(&raw); err != nil {
				return httpjson.NotFound("Draft")
			}
			old := []File{}
			_ = json.Unmarshal(raw, &old)
			for _, f := range old {
				if textclean.Contains(in.Keep, f.ID) {
					files = append(files, f)
				}
			}
		} else {
			var n int
			_ = tx.QueryRow(r.Context(), `SELECT count(*) FROM form_draft WHERE user_id=$1 AND form=$2`, u.ID, in.Form).Scan(&n)
			if n >= maxDrafts {
				return httpjson.Bad("You already have %d drafts of this form. Finish or discard some first.", maxDrafts)
			}
		}
		if len(files)+len(news) > maxFiles {
			return httpjson.Bad("A draft can keep at most %d files.", maxFiles)
		}
		for i, f := range news {
			fid, err := s.Files.Save(r.Context(), tx, u, f, "drafts", false)
			if err != nil {
				return err
			}
			files = append(files, File{Field: newsField[i], ID: fid, Name: f.Name})
		}
		fj, _ := json.Marshal(files)
		if in.ID != 0 {
			id = in.ID
			_, err := tx.Exec(r.Context(), `UPDATE form_draft SET title=$3, fields=$4, files=$5, ref_id=$6, updated_at=now() WHERE id=$1 AND user_id=$2`,
				in.ID, u.ID, in.Title, in.Fields, fj, in.RefID)
			return err
		}
		return tx.QueryRow(r.Context(), `INSERT INTO form_draft(user_id, form, ref_id, title, fields, files) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
			u.ID, in.Form, in.RefID, in.Title, in.Fields, fj).Scan(&id)
	})
	if err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"id": id, "files": files, "message": "Draft saved. Open it again from the same form to finish it."})
}

// remove discards a draft, or clears it once the form it belongs to is submitted.
func (s *Service) remove(w http.ResponseWriter, r *http.Request) error {
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `DELETE FROM form_draft WHERE id=$1 AND user_id=$2`, id, access.From(r).ID); err != nil {
		return err
	}
	return httpjson.OK(w, map[string]any{"message": "Draft discarded"})
}
