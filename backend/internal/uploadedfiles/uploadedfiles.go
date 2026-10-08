// Package uploadedfiles saves files people attach (invoices, item photos, bank proofs) and serves them back.
// Bank proofs are restricted to Finance and the System Admin.
package uploadedfiles

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"purchasedesk/internal/access"
	"purchasedesk/internal/database"
	"purchasedesk/internal/filestorage"
	"purchasedesk/internal/httpjson"
)

const MaxBytes = 15 << 20

var allowed = map[string]string{
	".pdf": "application/pdf", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".gif": "image/gif", ".bmp": "image/bmp", ".webp": "image/webp", ".heic": "image/heic",
}

type Service struct {
	DB    *pgxpool.Pool
	Store filestorage.Store
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/files/{id}", httpjson.Wrap(s.download))
}

type File struct {
	ID          int64
	Name        string
	ContentType string
	Data        []byte
}

// Read loads an uploaded multipart file and checks its type and size.
func Read(fh *multipart.FileHeader) (*File, error) {
	if fh == nil {
		return nil, nil
	}
	if fh.Size > MaxBytes {
		return nil, httpjson.Bad("%s is larger than 15 MB.", fh.Filename)
	}
	ext := strings.ToLower(path.Ext(fh.Filename))
	ct, ok := allowed[ext]
	if !ok {
		return nil, httpjson.Bad("%s: only PDF and image files can be attached.", fh.Filename)
	}
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	return &File{Name: path.Base(fh.Filename), ContentType: ct, Data: data}, nil
}

// Save stores the file and records it. folder groups files, for example "invoices" or "bank-proofs".
func (s *Service) Save(ctx context.Context, q database.Q, u *access.User, f *File, folder string, restricted bool) (int64, error) {
	if f == nil {
		return 0, nil
	}
	rb := make([]byte, 8)
	_, _ = rand.Read(rb)
	key := fmt.Sprintf("%s/%s/%s-%s", folder, time.Now().Format("2006/01"), hex.EncodeToString(rb), safeName(f.Name))
	if err := s.Store.Put(ctx, key, f.ContentType, f.Data); err != nil {
		return 0, err
	}
	var id int64
	err := q.QueryRow(ctx, `INSERT INTO file_object(storage_key,file_name,content_type,size_bytes,restricted,created_by) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		key, f.Name, f.ContentType, len(f.Data), restricted, u.ID).Scan(&id)
	return id, err
}

func safeName(n string) string {
	out := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, n)
	if len(out) > 80 {
		out = out[len(out)-80:]
	}
	return out
}

// Load returns a stored file's bytes, for example to attach an invoice to a Zoho bill.
func (s *Service) Load(ctx context.Context, id int64) (*File, error) {
	var key string
	f := &File{ID: id}
	if err := s.DB.QueryRow(ctx, `SELECT storage_key, file_name, content_type FROM file_object WHERE id=$1`, id).Scan(&key, &f.Name, &f.ContentType); err != nil {
		return nil, err
	}
	rc, err := s.Store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	f.Data, err = io.ReadAll(rc)
	return f, err
}

func (s *Service) download(w http.ResponseWriter, r *http.Request) error {
	u := access.From(r)
	id, err := httpjson.ID(r, "id")
	if err != nil {
		return err
	}
	var key, name, ct string
	var restricted bool
	if err := s.DB.QueryRow(r.Context(), `SELECT storage_key, file_name, content_type, restricted FROM file_object WHERE id=$1`, id).Scan(&key, &name, &ct, &restricted); err != nil {
		return err
	}
	if restricted && !u.IsAdmin && !u.HasStep("finance") {
		return httpjson.Forbidden("Bank proofs are visible only to Finance and the System Admin.")
	}
	rc, err := s.Store.Get(r.Context(), key)
	if err != nil {
		return httpjson.NotFound("File")
	}
	defer rc.Close()
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, safeName(name)))
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, err = io.Copy(w, rc)
	return err
}
