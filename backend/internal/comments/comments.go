// Package comments implements comments on articles and ideas; every comment shows its author's nickname and @slug.
package comments

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/httpx"
	"beeloy.com/backend/internal/platform/ids"
	"beeloy.com/backend/internal/platform/outbox"
	"beeloy.com/backend/internal/platform/pagination"
	"beeloy.com/backend/internal/platform/ratelimit"
	"beeloy.com/backend/internal/registry"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Author struct {
	UserID    *uuid.UUID `json:"user_id"`
	Nickname  string     `json:"nickname"`
	Slug      string     `json:"slug"`
	AvatarURL *string    `json:"avatar_url"`
}

type Comment struct {
	ID         uuid.UUID  `json:"id"`
	TargetType string     `json:"target_type"`
	TargetID   uuid.UUID  `json:"target_id"`
	ParentID   *uuid.UUID `json:"parent_id"`
	Author     Author     `json:"author"`
	Body       string     `json:"body"`
	Status     string     `json:"status"`
	ReplyCount int        `json:"reply_count"`
	CreatedAt  time.Time  `json:"created_at"`
}

type CommentWithTarget struct {
	Comment
	TargetSlug string `json:"target_slug"`
}

type CreateReq struct {
	TargetType string     `json:"target_type"`
	TargetID   uuid.UUID  `json:"target_id"`
	ParentID   *uuid.UUID `json:"parent_id"`
	Body       string     `json:"body"`
}

const select_ = `SELECT c.id, c.target_type, c.target_id, c.parent_id, c.author_id, COALESCE(u.nickname,''), COALESCE(us.slug,''), u.avatar_url, COALESCE(u.status,'deleted'),
	CASE WHEN c.status = 'visible' THEN c.body ELSE '' END, c.status,
	(SELECT count(*) FROM comments r WHERE r.parent_id = c.id AND r.status = 'visible'), c.created_at
	FROM comments c LEFT JOIN users u ON u.id = c.author_id
	LEFT JOIN slugs us ON us.entity_type = 'user' AND us.entity_id = u.id AND us.is_current`

func scan(r interface{ Scan(...any) error }) (Comment, error) {
	var c Comment
	var ustatus string
	err := r.Scan(&c.ID, &c.TargetType, &c.TargetID, &c.ParentID, &c.Author.UserID, &c.Author.Nickname, &c.Author.Slug, &c.Author.AvatarURL, &ustatus, &c.Body, &c.Status, &c.ReplyCount, &c.CreatedAt)
	if c.Author.UserID == nil || ustatus == "deleted" {
		c.Author = Author{Nickname: "Deleted user"}
	}
	return c, err
}

type Service struct {
	Pool     *pgxpool.Pool
	Authz    acl.Authorizer
	Audit    audit.Service
	Lim      *ratelimit.Limiter
	Registry registry.Repo
	Kick     func()
}

var (
	ruleMinute = ratelimit.Rule{Name: "comment_min", Limit: 10, Window: time.Minute}
	ruleDay    = ratelimit.Rule{Name: "comment_day", Limit: 100, Window: 24 * time.Hour}
	urlRe      = regexp.MustCompile(`(?i)https?://|www\.`)
)

func clean(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, s))
}

func (s *Service) Get(ctx context.Context, q db.DBTX, id uuid.UUID) (Comment, error) {
	c, err := scan(q.QueryRow(ctx, select_+` WHERE c.id = $1`, id))
	if db.IsNoRows(err) {
		return c, apperr.ErrNotFound
	}
	return c, err
}

func (s *Service) Create(ctx context.Context, subj acl.Subject, req CreateReq) (Comment, error) {
	if err := s.Authz.Require(&subj, acl.PermCommentCreate, acl.Resource{}); err != nil {
		return Comment{}, err
	}
	body := clean(req.Body)
	switch n := utf8.RuneCountInString(body); {
	case n == 0:
		return Comment{}, apperr.Field("body", "required")
	case n > 2000:
		return Comment{}, apperr.Field("body", "too_long")
	}
	if req.TargetType != "article" && req.TargetType != "idea" {
		return Comment{}, apperr.Field("target_type", "invalid_value")
	}
	res, err := s.Registry.Get(ctx, s.Pool, req.TargetID)
	if err != nil || string(res.Type) != req.TargetType || !res.PubliclyVisible() {
		return Comment{}, apperr.ErrNotFound
	}
	if req.ParentID != nil {
		p, err := s.Get(ctx, s.Pool, *req.ParentID)
		if err != nil || p.TargetID != req.TargetID || p.Status != "visible" || p.ParentID != nil {
			return Comment{}, apperr.Conflict("invalid_parent")
		}
	}
	var created time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT created_at FROM users WHERE id = $1`, subj.UserID).Scan(&created); err != nil {
		return Comment{}, err
	}
	if time.Since(created) < 24*time.Hour && len(urlRe.FindAllString(body, -1)) > 1 {
		return Comment{}, apperr.Field("body", "links_not_allowed")
	}
	if s.Lim != nil {
		if err := s.Lim.Check(ctx, ruleMinute, subj.UserID.String(), true); err != nil {
			return Comment{}, err
		}
		if err := s.Lim.Check(ctx, ruleDay, subj.UserID.String(), true); err != nil {
			return Comment{}, err
		}
	}
	id := ids.New()
	err = db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO comments (id, target_type, target_id, parent_id, author_id, body) VALUES ($1,$2,$3,$4,$5,$6)`,
			id, req.TargetType, req.TargetID, req.ParentID, subj.UserID, body); err != nil {
			return err
		}
		if err := s.Registry.Upsert(ctx, tx, registry.FromComment(id, &subj.UserID, req.TargetID, "visible")); err != nil {
			return err
		}
		return outbox.Add(ctx, tx, outbox.Event{Type: "comment.created", AggregateType: "comment", AggregateID: id,
			Payload: map[string]any{"target_type": req.TargetType, "target_id": req.TargetID, "author_id": subj.UserID}})
	})
	if err != nil {
		return Comment{}, err
	}
	if s.Kick != nil {
		s.Kick()
	}
	return s.Get(ctx, s.Pool, id)
}

func (s *Service) page(items []Comment, limit int, asc bool) ([]Comment, string) {
	page, more := pagination.Page(items, limit)
	next := ""
	if more {
		last := page[len(page)-1]
		next = pagination.Encode(pagination.Cursor{T: last.CreatedAt, ID: last.ID})
	}
	return page, next
}

// List returns top-level comments, oldest first.
func (s *Service) List(ctx context.Context, targetType string, targetID uuid.UUID, cur pagination.Cursor, limit int) ([]Comment, string, error) {
	if targetType != "article" && targetType != "idea" {
		return nil, "", apperr.Field("target_type", "invalid_value")
	}
	if res, ok, err := s.Registry.PublicNow(ctx, s.Pool, targetID); err != nil {
		return nil, "", err
	} else if !ok || string(res.Type) != targetType {
		return nil, "", apperr.ErrNotFound // the current state of the target, checked on every request
	}
	var curT *time.Time
	if !cur.IsZero() {
		curT = &cur.T
	}
	rows, err := s.Pool.Query(ctx, select_+` WHERE c.target_type = $1 AND c.target_id = $2 AND c.parent_id IS NULL
		AND (c.status <> 'deleted' OR EXISTS (SELECT 1 FROM comments r WHERE r.parent_id = c.id AND r.status = 'visible'))`, targetType, targetID)
	if err != nil {
		return nil, "", err
	}
	all, err := collect(rows)
	if err != nil {
		return nil, "", err
	}
	// keyset filter + ordering in Go keeps the SQL readable (threads are small)
	out := []Comment{}
	for _, c := range sortAsc(all) {
		if curT != nil && !(c.CreatedAt.After(*curT) || (c.CreatedAt.Equal(*curT) && c.ID.String() > cur.ID.String())) {
			continue
		}
		out = append(out, c)
		if len(out) > limit {
			break
		}
	}
	page, next := s.page(out, limit, true)
	return page, next, nil
}

func (s *Service) Replies(ctx context.Context, parentID uuid.UUID, cur pagination.Cursor, limit int) ([]Comment, string, error) {
	if parent, err := s.Get(ctx, s.Pool, parentID); err != nil {
		return nil, "", err
	} else if _, ok, err := s.Registry.PublicNow(ctx, s.Pool, parent.TargetID); err != nil {
		return nil, "", err
	} else if !ok {
		return nil, "", apperr.ErrNotFound
	}
	rows, err := s.Pool.Query(ctx, select_+` WHERE c.parent_id = $1 AND c.status <> 'deleted'`, parentID)
	if err != nil {
		return nil, "", err
	}
	all, err := collect(rows)
	if err != nil {
		return nil, "", err
	}
	out := []Comment{}
	for _, c := range sortAsc(all) {
		if !cur.IsZero() && !(c.CreatedAt.After(cur.T) || (c.CreatedAt.Equal(cur.T) && c.ID.String() > cur.ID.String())) {
			continue
		}
		out = append(out, c)
		if len(out) > limit {
			break
		}
	}
	page, next := s.page(out, limit, true)
	return page, next, nil
}

func collect(rows pgx.Rows) ([]Comment, error) {
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func sortAsc(cs []Comment) []Comment {
	sort.Slice(cs, func(i, j int) bool {
		if !cs[i].CreatedAt.Equal(cs[j].CreatedAt) {
			return cs[i].CreatedAt.Before(cs[j].CreatedAt)
		}
		return cs[i].ID.String() < cs[j].ID.String()
	})
	return cs
}

func (s *Service) setStatus(ctx context.Context, id uuid.UUID, status string, by *uuid.UUID, clearBody bool, act *audit.Input) error {
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var author *uuid.UUID
		var target uuid.UUID
		err := tx.QueryRow(ctx, `UPDATE comments SET status = $2, hidden_by = $3, body = CASE WHEN $4 THEN '' ELSE body END, updated_at = now()
			WHERE id = $1 RETURNING author_id, target_id`, id, status, by, clearBody).Scan(&author, &target)
		if db.IsNoRows(err) {
			return apperr.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := s.Registry.Upsert(ctx, tx, registry.FromComment(id, author, target, status)); err != nil {
			return err
		}
		if act != nil {
			return s.Audit.Record(ctx, tx, *act)
		}
		return nil
	})
}

// Delete: the author deletes their own comment (body wiped); a moderator may delete any.
func (s *Service) Delete(ctx context.Context, subj acl.Subject, id uuid.UUID) error {
	c, err := s.Get(ctx, s.Pool, id)
	if err != nil {
		return err
	}
	own := c.Author.UserID != nil && *c.Author.UserID == subj.UserID && subj.Status == "active"
	if !own && !s.Authz.Can(subj, acl.PermCommentModerate, acl.Resource{}) {
		return apperr.ErrForbidden
	}
	return s.setStatus(ctx, id, "deleted", nil, true, nil)
}

func (s *Service) Hide(ctx context.Context, subj acl.Subject, id uuid.UUID, unhide bool) error {
	if err := s.Authz.Require(&subj, acl.PermCommentModerate, acl.Resource{}); err != nil {
		return err
	}
	status, action := "hidden", audit.ActionCommentHide
	if unhide {
		status, action = "visible", audit.ActionCommentUnhide
	}
	cur, err := s.Get(ctx, s.Pool, id)
	if err != nil {
		return err
	}
	if cur.Status == "deleted" {
		return apperr.Conflict("deleted")
	}
	return s.setStatus(ctx, id, status, &subj.UserID, false, &audit.Input{ActorID: &subj.UserID, Action: action, TargetType: "comment", TargetID: id.String()})
}

// ByAuthor lists a user's visible comments on public targets (profile activity).
func (s *Service) ByAuthor(ctx context.Context, authorID uuid.UUID, cur pagination.Cursor, limit int) ([]CommentWithTarget, string, error) {
	var curT *time.Time
	if !cur.IsZero() {
		curT = &cur.T
	}
	rows, err := s.Pool.Query(ctx, select_+` JOIN resources tr ON tr.id = c.target_id AND tr.status = 'active' AND tr.visibility = 'public'
		WHERE c.author_id = $1 AND c.status = 'visible' AND ($2::timestamptz IS NULL OR (c.created_at, c.id) < ($2, $3))
		ORDER BY c.created_at DESC, c.id DESC LIMIT $4`, authorID, curT, cur.ID, limit+1)
	if err != nil {
		return nil, "", err
	}
	all, err := collect(rows)
	if err != nil {
		return nil, "", err
	}
	page, more := pagination.Page(all, limit)
	out := make([]CommentWithTarget, 0, len(page))
	for _, c := range page {
		var slug string
		_ = s.Pool.QueryRow(ctx, `SELECT slug FROM slugs WHERE entity_type = $1 AND entity_id = $2 AND is_current`, c.TargetType, c.TargetID).Scan(&slug)
		out = append(out, CommentWithTarget{Comment: c, TargetSlug: slug})
	}
	next := ""
	if more {
		last := page[len(page)-1]
		next = pagination.Encode(pagination.Cursor{T: last.CreatedAt, ID: last.ID})
	}
	return out, next, nil
}

// ---- HTTP ----

type Handler struct {
	Svc   *Service
	Users identity.Repo
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/comments", h.list)
	r.Get("/comments/{id}/replies", h.replies)
	r.Get("/users/{slug}/comments", h.byAuthor)
	r.Group(func(r chi.Router) {
		r.Use(identity.RequireAuth, identity.RequireActive)
		r.Post("/comments", h.create)
		r.Delete("/comments/{id}", h.delete)
		r.Post("/mod/comments/{id}/hide", h.hide(false))
		r.Post("/mod/comments/{id}/unhide", h.hide(true))
	})
}

func listParams(r *http.Request) (pagination.Cursor, int, error) {
	limit, err := pagination.ParseLimit(r.URL.Query())
	if err != nil {
		return pagination.Cursor{}, 0, err
	}
	cur, err := pagination.Decode(r.URL.Query().Get("cursor"))
	return cur, limit, err
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	cur, limit, err := listParams(r)
	var tid uuid.UUID
	if err == nil {
		tid, err = uuid.Parse(r.URL.Query().Get("target_id"))
		if err != nil {
			err = apperr.Field("target_id", "invalid_value")
		}
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, next, err := h.Svc.List(r.Context(), r.URL.Query().Get("target_type"), tid, cur, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSONList(w, items, next)
}

func (h *Handler) replies(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	cur, limit, err2 := listParams(r)
	if err != nil || err2 != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	items, next, err := h.Svc.Replies(r.Context(), id, cur, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSONList(w, items, next)
}

func (h *Handler) byAuthor(w http.ResponseWriter, r *http.Request) {
	u, _, err := h.Users.GetUserBySlug(r.Context(), h.Svc.Pool, identity.Normalize(chi.URLParam(r, "slug")))
	if err != nil || u.Status != identity.StatusActive {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	if !u.ProfilePublic {
		httpx.JSONList(w, []CommentWithTarget{}, "")
		return
	}
	cur, limit, err := listParams(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, next, err := h.Svc.ByAuthor(r.Context(), u.ID, cur, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSONList(w, items, next)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req CreateReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.Svc.Create(r.Context(), identity.MustSubject(r), req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, c)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err == nil {
		err = h.Svc.Delete(r.Context(), identity.MustSubject(r), id)
	} else {
		err = apperr.ErrNotFound
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) hide(unhide bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err == nil {
			err = h.Svc.Hide(r.Context(), identity.MustSubject(r), id, unhide)
		} else {
			err = apperr.ErrNotFound
		}
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.NoContent(w)
	}
}
