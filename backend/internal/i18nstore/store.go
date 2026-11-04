// Package i18nstore serves the active UI dictionaries (activated by Content Sync) from PostgreSQL with caching.
package i18nstore

import (
	"context"
	_ "embed"
	"net/http"
	"strings"
	"sync"
	"time"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

//go:embed fallback_en.json
var fallbackEN []byte

// FallbackEN returns the embedded English reference dictionary (a copy).
func FallbackEN() []byte { return append([]byte(nil), fallbackEN...) }

const versionTTL = 30 * time.Second

type Store struct {
	Pool *pgxpool.Pool
	RDB  *redis.Client

	mu      sync.RWMutex
	version string
	checked time.Time
}

type Meta struct {
	Version        string   `json:"version"`
	Locales        []string `json:"locales"`
	DefaultLocale  string   `json:"default_locale"`
	FallbackLocale string   `json:"fallback_locale"`
}

// Invalidate drops the in-process version cache (used after a local activation).
func (s *Store) Invalidate() { s.mu.Lock(); s.checked = time.Time{}; s.mu.Unlock() }

// ActiveVersion returns "" when no release has been activated yet.
func (s *Store) ActiveVersion(ctx context.Context) (string, error) {
	s.mu.RLock()
	if time.Since(s.checked) < versionTTL {
		v := s.version
		s.mu.RUnlock()
		return v, nil
	}
	s.mu.RUnlock()
	var v string
	err := s.Pool.QueryRow(ctx, `SELECT version FROM i18n_active WHERE id = 1`).Scan(&v)
	if err != nil && !db.IsNoRows(err) {
		return "", err
	}
	s.mu.Lock()
	s.version, s.checked = v, time.Now()
	s.mu.Unlock()
	return v, nil
}

// Bundle returns the JSON body, its ETag (sha256 of the published file) and the version.
// Without an active release the embedded English fallback is returned with version "" .
func (s *Store) Bundle(ctx context.Context, locale string) (body []byte, etag, version string, err error) {
	if !locales.Known(locale) {
		return nil, "", "", apperr.ErrNotFound
	}
	version, err = s.ActiveVersion(ctx)
	if err != nil {
		return nil, "", "", err
	}
	if version == "" {
		return fallbackEN, "fallback", "", nil
	}
	key := "i18n:" + version + ":" + locale
	if s.RDB != nil {
		if b, e := s.RDB.Get(ctx, key).Bytes(); e == nil {
			if i := strings.IndexByte(string(b), '\n'); i > 0 {
				return b[i+1:], string(b[:i]), version, nil
			}
		}
	}
	var content []byte
	err = s.Pool.QueryRow(ctx, `SELECT content::text, sha256 FROM i18n_bundles WHERE version = $1 AND locale = $2`, version, locale).Scan(&content, &etag)
	if db.IsNoRows(err) {
		return nil, "", "", apperr.ErrNotFound
	}
	if err != nil {
		return nil, "", "", err
	}
	if s.RDB != nil {
		_ = s.RDB.Set(ctx, key, append([]byte(etag+"\n"), content...), time.Hour).Err()
	}
	return content, etag, version, nil
}

func (s *Store) Meta(ctx context.Context) (Meta, error) {
	v, err := s.ActiveVersion(ctx)
	return Meta{Version: v, Locales: locales.Codes(), DefaultLocale: locales.Default(), FallbackLocale: locales.Fallback()}, err
}

type Handler struct{ S *Store }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/i18n/meta", h.meta)
	r.Get("/i18n/{locale}", h.bundle)
}

func (h *Handler) meta(w http.ResponseWriter, r *http.Request) {
	m, err := h.S.Meta(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, m)
}

// bundle answers with the flat dictionary (no {"data": …} envelope).
func (h *Handler) bundle(w http.ResponseWriter, r *http.Request) {
	body, etag, version, err := h.S.Bundle(r.Context(), chi.URLParam(r, "locale"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	quoted := `"` + etag + `"`
	w.Header().Set("ETag", quoted)
	w.Header().Set("Cache-Control", "public, max-age=300")
	if version == "" {
		w.Header().Set("X-I18n-Fallback", "1")
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" && (inm == quoted || inm == "W/"+quoted) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(body)
}
