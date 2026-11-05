// Package seo serves the sitemap data (public resources only; never private profiles or drafts).
package seo

import (
	"net/http"
	"time"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/articles"
	"beeloy.com/backend/internal/platform/httpx"
	"beeloy.com/backend/internal/platform/pagination"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Entry struct {
	Slug      string    `json:"path_slug"`
	UpdatedAt time.Time `json:"updated_at"`
	Locales   []string  `json:"locales"`
	Category  string    `json:"category_key,omitempty"`
}

type Handler struct {
	Pool     *pgxpool.Pool
	Articles articles.Repo
}

func (h *Handler) Routes(r chi.Router) { r.Get("/seo/sitemap-entries", h.entries) }

func (h *Handler) entries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := pagination.ParseLimitMax(q, 100, 500)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	cur, err := pagination.Decode(q.Get("cursor"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	type row struct {
		id uuid.UUID
		e  Entry
	}
	var rows []row
	switch q.Get("type") {
	case "articles":
		items, err := h.Articles.SitemapEntries(r.Context(), h.Pool, cur, limit+1)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		for _, it := range items {
			rows = append(rows, row{it.ID, Entry{it.Slug, it.UpdatedAt, it.Locales, it.Category}})
		}
	case "ideas", "users":
		sql := `SELECT i.id, s.slug, i.updated_at FROM ideas i JOIN slugs s ON s.entity_type='idea' AND s.entity_id=i.id AND s.is_current`
		if q.Get("type") == "users" {
			sql = `SELECT u.id, s.slug, u.updated_at FROM users u JOIN slugs s ON s.entity_type='user' AND s.entity_id=u.id AND s.is_current
				WHERE u.status='active' AND u.profile_public`
		}
		var curT *time.Time
		if !cur.IsZero() {
			curT = &cur.T
		}
		joiner := " WHERE"
		if q.Get("type") == "users" {
			joiner = " AND"
		}
		col := "i"
		if q.Get("type") == "users" {
			col = "u"
		}
		res, err := h.Pool.Query(r.Context(), sql+joiner+` ($1::timestamptz IS NULL OR (`+col+`.updated_at, `+col+`.id) < ($1, $2)) ORDER BY `+col+`.updated_at DESC, `+col+`.id DESC LIMIT $3`, curT, cur.ID, limit+1)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		defer res.Close()
		for res.Next() {
			var x row
			if err := res.Scan(&x.id, &x.e.Slug, &x.e.UpdatedAt); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			x.e.Locales = []string{}
			rows = append(rows, x)
		}
	default:
		httpx.WriteError(w, r, apperr.ErrBadRequest.WithMessage("bad type"))
		return
	}
	page, more := pagination.Page(rows, limit)
	out := make([]Entry, len(page))
	for i, p := range page {
		out[i] = p.e
	}
	next := ""
	if more {
		last := page[len(page)-1]
		next = pagination.Encode(pagination.Cursor{T: last.e.UpdatedAt, ID: last.id})
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSONList(w, out, next)
}
