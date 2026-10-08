// Package filestorage keeps uploaded files (invoices, photos, bank proofs).
// Local disk is used until Supabase credentials are given.
package filestorage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Store interface {
	Put(ctx context.Context, key, contentType string, data []byte) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Name() string
}

type Local struct{ Dir string }

func (l Local) Name() string { return "local" }

func (l Local) path(key string) (string, error) {
	clean := filepath.Clean("/" + key)
	if strings.Contains(clean, "..") {
		return "", fmt.Errorf("bad key")
	}
	return filepath.Join(l.Dir, clean), nil
}

func (l Local) Put(_ context.Context, key, _ string, data []byte) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o640)
}

func (l Local) Get(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// Supabase stores objects in a private bucket through the Storage REST API using the service key.
type Supabase struct {
	URL, Key, Bucket string
	HTTP             *http.Client
}

func NewSupabase(u, key, bucket string) *Supabase {
	return &Supabase{URL: u, Key: key, Bucket: bucket, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

func (s *Supabase) Name() string { return "supabase" }

func (s *Supabase) objectURL(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return fmt.Sprintf("%s/storage/v1/object/%s/%s", s.URL, url.PathEscape(s.Bucket), strings.Join(parts, "/"))
}

func (s *Supabase) Put(ctx context.Context, key, contentType string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.objectURL(key), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Key)
	req.Header.Set("apikey", s.Key)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-upsert", "false")
	res, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("supabase upload failed: %s %s", res.Status, string(b))
	}
	return nil
}

func (s *Supabase) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL(key), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Key)
	req.Header.Set("apikey", s.Key)
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		res.Body.Close()
		return nil, fmt.Errorf("supabase download failed: %s", res.Status)
	}
	return res.Body, nil
}
