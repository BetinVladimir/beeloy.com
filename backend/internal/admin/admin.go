// Package admin exposes administrator endpoints: user list, role bindings, audit log.
package admin

import (
	"net/http"
	"strings"
	"time"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/platform/httpx"
	"beeloy.com/backend/internal/platform/pagination"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type UserRow struct {
	ID        uuid.UUID          `json:"id"`
	Nickname  string             `json:"nickname"`
	Slug      string             `json:"slug"`
	Status    string             `json:"status"`
	Roles     []identity.RoleDTO `json:"roles"`
	CreatedAt time.Time          `json:"created_at"`
}

type Handler struct {
	Pool   *pgxpool.Pool
	Authz  acl.Authorizer
	Grants *acl.GrantService
	Audit  audit.Service
}

func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(identity.RequireAuth, identity.RequireActive)
		r.Get("/admin/users", h.users)
		r.Post("/admin/role-bindings", h.grant)
		r.Delete("/admin/role-bindings/{id}", h.revoke)
		r.Get("/admin/audit", h.auditLog)
	})
}

func (h *Handler) users(w http.ResponseWriter, r *http.Request) {
	subj := identity.MustSubject(r)
	if err := h.Authz.Require(&subj, acl.PermUserList, acl.Resource{}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit, err := pagination.ParseLimit(r.URL.Query())
	var cur pagination.Cursor
	if err == nil {
		cur, err = pagination.Decode(r.URL.Query().Get("cursor"))
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(r.URL.Query().Get("q"), "@")))
	pat := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
	var curT *time.Time
	if !cur.IsZero() {
		curT = &cur.T
	}
	rows, err := h.Pool.Query(r.Context(), `SELECT u.id, u.nickname, COALESCE(s.slug,''), u.status, u.created_at,
		COALESCE((SELECT json_agg(json_build_object('role', rb.role, 'scope_type', rb.scope_type, 'scope_id', rb.scope_id)) FROM role_bindings rb WHERE rb.user_id = u.id), '[]'::json)
		FROM users u LEFT JOIN slugs s ON s.entity_type='user' AND s.entity_id=u.id AND s.is_current
		WHERE ($1 = '%' OR s.slug ILIKE $1 OR u.nickname ILIKE $1)
		  AND ($2::timestamptz IS NULL OR (u.created_at, u.id) < ($2, $3))
		ORDER BY u.created_at DESC, u.id DESC LIMIT $4`, pat, curT, cur.ID, limit+1)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	out := []UserRow{}
	for rows.Next() {
		var u UserRow
		if err := rows.Scan(&u.ID, &u.Nickname, &u.Slug, &u.Status, &u.CreatedAt, &u.Roles); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, u)
	}
	page, more := pagination.Page(out, limit)
	next := ""
	if more {
		last := page[len(page)-1]
		next = pagination.Encode(pagination.Cursor{T: last.CreatedAt, ID: last.ID})
	}
	httpx.JSONList(w, page, next)
}

func (h *Handler) grant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID    uuid.UUID     `json:"user_id"`
		Role      acl.Role      `json:"role"`
		ScopeType acl.ScopeType `json:"scope_type"`
		ScopeID   *uuid.UUID    `json:"scope_id"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	b, err := h.Grants.Grant(r.Context(), identity.MustSubject(r), httpx.ClientIP(r), req.UserID, req.Role, req.ScopeType, req.ScopeID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, b)
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	if err := h.Grants.Revoke(r.Context(), identity.MustSubject(r), httpx.ClientIP(r), id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) auditLog(w http.ResponseWriter, r *http.Request) {
	subj := identity.MustSubject(r)
	if err := h.Authz.Require(&subj, acl.PermAuditView, acl.Resource{}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit, err := pagination.ParseLimit(r.URL.Query())
	var cur pagination.Cursor
	if err == nil {
		cur, err = pagination.Decode(r.URL.Query().Get("cursor"))
	}
	var actor *uuid.UUID
	if v := r.URL.Query().Get("actor_id"); err == nil && v != "" {
		id, e := uuid.Parse(v)
		if e != nil {
			err = apperr.ErrBadRequest.WithMessage("bad actor_id")
		}
		actor = &id
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	evs, next, err := h.Audit.List(r.Context(), h.Pool, audit.Filter{ActorID: actor, Action: r.URL.Query().Get("action")}, cur, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSONList(w, evs, next)
}
