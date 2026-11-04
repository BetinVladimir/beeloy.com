// Package search provides search behind a Provider interface (PostgreSQL implementation for the MVP).
package search

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/httpx"
	"beeloy.com/backend/internal/platform/pagination"
	"beeloy.com/backend/internal/platform/ratelimit"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxOffset = 200

type ArticleQuery struct {
	Text, Locale string
	Offset       int
	Mode         string // "f" (full-text) or "t" (trigram)
	Limit        int
}

type PeopleQuery struct {
	Text   string
	Offset int
	Limit  int
}

type ArticleHit struct {
	ID          uuid.UUID `json:"id"`
	Slug        string    `json:"slug"`
	CategoryKey string    `json:"category_key"`
	Locale      string    `json:"locale"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary"`
	CoverURL    *string   `json:"cover_url"`
	PublishedAt time.Time `json:"published_at"`
}

type PersonHit struct {
	UserID    uuid.UUID `json:"id"`
	Nickname  string    `json:"nickname"`
	Slug      string    `json:"slug"`
	AvatarURL *string   `json:"avatar_url"`
}

// Provider lets the engine be replaced later without changing the API.
type Provider interface {
	// SearchArticles returns up to Limit hits and the mode actually used ("f" or "t").
	SearchArticles(ctx context.Context, q ArticleQuery) ([]ArticleHit, string, error)
	SearchPeople(ctx context.Context, q PeopleQuery) ([]PersonHit, error)
}

type PGProvider struct{ Pool *pgxpool.Pool }

// escapeLike escapes LIKE metacharacters (backslash is the escape char).
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

const articleSelect = `SELECT a.id, s.slug, c.key, al.locale, al.title, al.summary, a.cover_url, a.published_at
	FROM article_locales al JOIN articles a ON a.id = al.article_id AND a.status = 'published' AND a.visibility = 'public'
	JOIN categories c ON c.id = a.category_id
	JOIN slugs s ON s.entity_type = 'article' AND s.entity_id = a.id AND s.is_current`

func (p *PGProvider) SearchArticles(ctx context.Context, q ArticleQuery) ([]ArticleHit, string, error) {
	run := func(sql string, args ...any) ([]ArticleHit, error) {
		rows, err := p.Pool.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []ArticleHit{}
		for rows.Next() {
			var h ArticleHit
			var pub *time.Time
			if err := rows.Scan(&h.ID, &h.Slug, &h.CategoryKey, &h.Locale, &h.Title, &h.Summary, &h.CoverURL, &pub); err != nil {
				return nil, err
			}
			if pub != nil {
				h.PublishedAt = *pub
			}
			out = append(out, h)
		}
		return out, rows.Err()
	}
	if q.Mode != "t" {
		hits, err := run(articleSelect+` WHERE al.locale = $1 AND al.search_vector @@ websearch_to_tsquery('simple', $2)
			ORDER BY ts_rank(al.search_vector, websearch_to_tsquery('simple', $2)) DESC, a.published_at DESC, a.id DESC LIMIT $3 OFFSET $4`,
			q.Locale, q.Text, q.Limit, q.Offset)
		if err != nil || len(hits) > 0 || q.Mode == "f" {
			return hits, "f", err
		}
	}
	hits, err := run(articleSelect+` WHERE al.locale = $1 AND (al.title || ' ' || al.summary) ILIKE '%' || $2 || '%'
		ORDER BY similarity(al.title, $3) DESC, a.published_at DESC, a.id DESC LIMIT $4 OFFSET $5`,
		q.Locale, escapeLike(q.Text), q.Text, q.Limit, q.Offset)
	return hits, "t", err
}

func (p *PGProvider) SearchPeople(ctx context.Context, q PeopleQuery) ([]PersonHit, error) {
	t := strings.ToLower(strings.TrimPrefix(q.Text, "@"))
	rows, err := p.Pool.Query(ctx, `SELECT u.id, u.nickname, s.slug, u.avatar_url
		FROM users u JOIN slugs s ON s.entity_type = 'user' AND s.entity_id = u.id AND s.is_current
		WHERE u.status = 'active' AND u.profile_public
		  AND (s.slug ILIKE '%' || $1 || '%' OR u.nickname ILIKE '%' || $1 || '%')
		ORDER BY (s.slug = $2) DESC, (s.slug ILIKE $1 || '%') DESC, similarity(u.nickname, $2) DESC, u.nickname, u.id
		LIMIT $3 OFFSET $4`, escapeLike(t), t, q.Limit, q.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PersonHit{}
	for rows.Next() {
		var h PersonHit
		if err := rows.Scan(&h.UserID, &h.Nickname, &h.Slug, &h.AvatarURL); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// cursor is "<mode>:<offset>" in base64url.
func encodeCursor(mode string, offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(mode + ":" + strconv.Itoa(offset)))
}

func decodeCursor(s string) (mode string, offset int, err error) {
	if s == "" {
		return "", 0, nil
	}
	b, e := base64.RawURLEncoding.DecodeString(s)
	parts := strings.SplitN(string(b), ":", 2)
	if e != nil || len(parts) != 2 || (parts[0] != "f" && parts[0] != "t") {
		return "", 0, apperr.ErrBadRequest.WithMessage("bad cursor")
	}
	offset, e = strconv.Atoi(parts[1])
	if e != nil || offset < 0 {
		return "", 0, apperr.ErrBadRequest.WithMessage("bad cursor")
	}
	return parts[0], offset, nil
}

func validateQuery(q string) (string, error) {
	q = strings.TrimSpace(q)
	if n := utf8.RuneCountInString(q); n < 2 || n > 100 {
		return "", apperr.Field("q", map[bool]string{true: "too_short", false: "too_long"}[n < 2])
	}
	return q, nil
}

type Handler struct {
	P   Provider
	Lim *ratelimit.Limiter
}

var ruleSearch = ratelimit.Rule{Name: "search", Limit: 30, Window: time.Minute}

func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.Lim.Middleware(ruleSearch, ratelimit.ByIP, false))
		r.Get("/search/articles", h.articles)
		r.Get("/search/people", h.people)
	})
}

func (h *Handler) articles(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	text, err := validateQuery(qs.Get("q"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	loc := qs.Get("locale")
	if !locales.Known(loc) {
		httpx.WriteError(w, r, apperr.Field("locale", "invalid_value"))
		return
	}
	limit, err := pagination.ParseLimitMax(qs, 20, 20)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	mode, offset, err := decodeCursor(qs.Get("cursor"))
	if err != nil || offset > maxOffset {
		httpx.WriteError(w, r, apperr.ErrBadRequest.WithMessage("bad cursor"))
		return
	}
	hits, used, err := h.P.SearchArticles(r.Context(), ArticleQuery{Text: text, Locale: loc, Offset: offset, Mode: mode, Limit: limit + 1})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, more := pagination.Page(hits, limit)
	next := ""
	if more && offset+limit <= maxOffset {
		next = encodeCursor(used, offset+limit)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSONList(w, page, next)
}

func (h *Handler) people(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	text, err := validateQuery(strings.TrimPrefix(strings.TrimSpace(qs.Get("q")), "@"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit, err := pagination.ParseLimitMax(qs, 20, 20)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_, offset, err := decodeCursor(qs.Get("cursor"))
	if err != nil || offset > maxOffset {
		httpx.WriteError(w, r, apperr.ErrBadRequest.WithMessage("bad cursor"))
		return
	}
	hits, err := h.P.SearchPeople(r.Context(), PeopleQuery{Text: text, Offset: offset, Limit: limit + 1})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, more := pagination.Page(hits, limit)
	next := ""
	if more && offset+limit <= maxOffset {
		next = encodeCursor("f", offset+limit)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSONList(w, page, next)
}
