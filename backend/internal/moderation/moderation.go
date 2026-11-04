// Package moderation handles reports (with a bounded snapshot of private messages) and account blocking.
package moderation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"time"
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

type Report struct {
	ID             uuid.UUID       `json:"id"`
	ReporterID     *uuid.UUID      `json:"reporter_id,omitempty"`
	TargetType     string          `json:"target_type"`
	TargetID       uuid.UUID       `json:"target_id"`
	Reason         string          `json:"reason"`
	Details        string          `json:"details"`
	Snapshot       json.RawMessage `json:"snapshot,omitempty"`
	Status         string          `json:"status"`
	ResolvedBy     *uuid.UUID      `json:"resolved_by,omitempty"`
	ResolvedAt     *time.Time      `json:"resolved_at,omitempty"`
	ResolutionNote string          `json:"resolution_note"`
	CreatedAt      time.Time       `json:"created_at"`
}

type CreateReq struct {
	TargetType string    `json:"target_type"`
	TargetID   uuid.UUID `json:"target_id"`
	Reason     string    `json:"reason"`
	Details    string    `json:"details"`
}

type Service struct {
	Pool     *pgxpool.Pool
	Authz    acl.Authorizer
	Audit    audit.Service
	Lim      *ratelimit.Limiter
	Sessions *identity.SessionService
	Registry registry.Repo
	Kick     func()
}

var ruleReport = ratelimit.Rule{Name: "report_create", Limit: 10, Window: 24 * time.Hour}

const reportCols = `id, reporter_id, target_type, target_id, reason, details, status, resolved_by, resolved_at, resolution_note, created_at`

func scanReport(r interface{ Scan(...any) error }, withSnapshot bool) (Report, error) {
	var x Report
	if withSnapshot {
		var snap []byte
		err := r.Scan(&x.ID, &x.ReporterID, &x.TargetType, &x.TargetID, &x.Reason, &x.Details, &x.Status, &x.ResolvedBy, &x.ResolvedAt, &x.ResolutionNote, &x.CreatedAt, &snap)
		x.Snapshot = snap
		return x, err
	}
	err := r.Scan(&x.ID, &x.ReporterID, &x.TargetType, &x.TargetID, &x.Reason, &x.Details, &x.Status, &x.ResolvedBy, &x.ResolvedAt, &x.ResolutionNote, &x.CreatedAt)
	return x, err
}

// Create files a report. For a message it stores a snapshot of up to 5 messages around it — the only way staff see private text.
func (s *Service) Create(ctx context.Context, subj acl.Subject, req CreateReq) (Report, bool, error) {
	if err := s.Authz.Require(&subj, acl.PermReportCreate, acl.Resource{}); err != nil {
		return Report{}, false, err
	}
	switch req.Reason {
	case "spam", "abuse", "illegal", "other":
	default:
		return Report{}, false, apperr.Field("reason", "invalid_value")
	}
	if utf8.RuneCountInString(req.Details) > 1000 {
		return Report{}, false, apperr.Field("details", "too_long")
	}
	var snapshot []byte
	switch req.TargetType {
	case "message":
		snap, err := s.messageSnapshot(ctx, subj.UserID, req.TargetID)
		if err != nil {
			return Report{}, false, err
		}
		snapshot = snap
	case "idea", "user", "article", "comment":
		res, visible, err := s.Registry.PublicNow(ctx, s.Pool, req.TargetID)
		if err != nil || string(res.Type) != req.TargetType || !visible {
			// user profiles with profile_public=false are 'restricted' in the registry but can still be reported
			if !(err == nil && string(res.Type) == req.TargetType && res.Type == registry.TypeUser && res.Status == registry.StatusActive) {
				return Report{}, false, apperr.ErrNotFound
			}
		}
	default:
		return Report{}, false, apperr.Field("target_type", "invalid_value")
	}
	// the same open report is returned instead of a duplicate
	if x, err := scanReport(s.Pool.QueryRow(ctx, `SELECT `+reportCols+` FROM reports WHERE reporter_id=$1 AND target_type=$2 AND target_id=$3 AND status='open'`,
		subj.UserID, req.TargetType, req.TargetID), false); err == nil {
		x.Snapshot, x.ReporterID = nil, nil
		return x, false, nil
	}
	if err := s.Lim.Check(ctx, ruleReport, subj.UserID.String(), true); err != nil {
		return Report{}, false, err
	}
	id := ids.New()
	err := db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO reports (id, reporter_id, target_type, target_id, reason, details, snapshot) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			id, subj.UserID, req.TargetType, req.TargetID, req.Reason, strings.TrimSpace(req.Details), snapshot); err != nil {
			return err
		}
		return outbox.Add(ctx, tx, outbox.Event{Type: "report.created", AggregateType: "report", AggregateID: id,
			Payload: map[string]any{"target_type": req.TargetType, "target_id": req.TargetID}})
	})
	if err != nil {
		return Report{}, false, err
	}
	if s.Kick != nil {
		s.Kick()
	}
	x, err := scanReport(s.Pool.QueryRow(ctx, `SELECT `+reportCols+` FROM reports WHERE id=$1`, id), false)
	x.ReporterID = nil
	return x, true, err
}

func (s *Service) messageSnapshot(ctx context.Context, reporter, messageID uuid.UUID) ([]byte, error) {
	var conv uuid.UUID
	var seq int64
	var sender *uuid.UUID
	err := s.Pool.QueryRow(ctx, `SELECT m.conversation_id, m.seq, m.sender_id FROM messages m
		JOIN conversation_members cm ON cm.conversation_id = m.conversation_id AND cm.user_id = $2 WHERE m.id = $1`, messageID, reporter).Scan(&conv, &seq, &sender)
	if db.IsNoRows(err) {
		return nil, apperr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if sender != nil && *sender == reporter {
		return nil, apperr.Field("target_id", "invalid_value") // own messages cannot be reported
	}
	rows, err := s.Pool.Query(ctx, `SELECT seq, sender_id, CASE WHEN deleted_at IS NULL THEN body ELSE '' END, created_at FROM messages
		WHERE conversation_id = $1 AND seq BETWEEN $2 AND $3 ORDER BY seq`, conv, seq-2, seq+2)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type item struct {
		Seq       int64      `json:"seq"`
		SenderID  *uuid.UUID `json:"sender_id"`
		Body      string     `json:"body"`
		CreatedAt time.Time  `json:"created_at"`
	}
	out := []item{}
	for rows.Next() {
		var i item
		if err := rows.Scan(&i.Seq, &i.SenderID, &i.Body, &i.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return json.Marshal(out)
}

// List returns reports without snapshots.
func (s *Service) List(ctx context.Context, subj acl.Subject, status string, cur pagination.Cursor, limit int) ([]Report, string, error) {
	if err := s.Authz.Require(&subj, acl.PermReportView, acl.Resource{}); err != nil {
		return nil, "", err
	}
	if status == "" {
		status = "open"
	}
	if status != "open" && status != "resolved" && status != "dismissed" {
		return nil, "", apperr.ErrBadRequest.WithMessage("bad status")
	}
	var curT *time.Time
	if !cur.IsZero() {
		curT = &cur.T
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+reportCols+` FROM reports WHERE status = $1 AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
		ORDER BY created_at DESC, id DESC LIMIT $4`, status, curT, cur.ID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []Report{}
	for rows.Next() {
		x, err := scanReport(rows, false)
		if err != nil {
			return nil, "", err
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	page, more := pagination.Page(out, limit)
	next := ""
	if more {
		last := page[len(page)-1]
		next = pagination.Encode(pagination.Cursor{T: last.CreatedAt, ID: last.ID})
	}
	return page, next, nil
}

// Get returns one report with its snapshot; every view of a snapshot is audited.
func (s *Service) Get(ctx context.Context, subj acl.Subject, ip netip.Addr, id uuid.UUID) (Report, error) {
	if err := s.Authz.Require(&subj, acl.PermReportView, acl.Resource{}); err != nil {
		return Report{}, err
	}
	x, err := scanReport(s.Pool.QueryRow(ctx, `SELECT `+reportCols+`, snapshot FROM reports WHERE id=$1`, id), true)
	if db.IsNoRows(err) {
		return x, apperr.ErrNotFound
	}
	if err != nil {
		return x, err
	}
	if len(x.Snapshot) > 0 {
		if err := s.Audit.Record(ctx, s.Pool, audit.Input{ActorID: &subj.UserID, Action: audit.ActionReportViewSnapshot, TargetType: "report", TargetID: id.String(), IP: ip}); err != nil {
			return x, err
		}
	}
	return x, nil
}

func (s *Service) Resolve(ctx context.Context, subj acl.Subject, ip netip.Addr, id uuid.UUID, resolution, note string) error {
	if err := s.Authz.Require(&subj, acl.PermReportResolve, acl.Resource{}); err != nil {
		return err
	}
	if resolution != "resolved" && resolution != "dismissed" {
		return apperr.Field("resolution", "invalid_value")
	}
	if utf8.RuneCountInString(note) > 500 {
		return apperr.Field("note", "too_long")
	}
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE reports SET status=$2, resolved_by=$3, resolved_at=now(), resolution_note=$4 WHERE id=$1 AND status='open'`, id, resolution, subj.UserID, strings.TrimSpace(note))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reports WHERE id=$1)`, id).Scan(&exists)
			if !exists {
				return apperr.ErrNotFound
			}
			return apperr.Conflict("already_resolved")
		}
		return s.Audit.Record(ctx, tx, audit.Input{ActorID: &subj.UserID, Action: audit.ActionReportResolve, TargetType: "report", TargetID: id.String(),
			Meta: map[string]any{"resolution": resolution}, IP: ip})
	})
}

// BlockAccount blocks a user (sessions are revoked at once). Administrators and oneself cannot be blocked.
func (s *Service) BlockAccount(ctx context.Context, subj acl.Subject, ip netip.Addr, userID uuid.UUID, reason string) error {
	if err := s.Authz.Require(&subj, acl.PermUserBlockAccount, acl.Resource{}); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) < 5 {
		return apperr.Field("reason", "too_short")
	}
	if userID == subj.UserID {
		return apperr.ErrForbidden
	}
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var status string
		var isAdmin bool
		err := tx.QueryRow(ctx, `SELECT u.status, EXISTS(SELECT 1 FROM role_bindings rb WHERE rb.user_id = u.id AND rb.role = 'admin') FROM users u WHERE u.id=$1 FOR UPDATE`, userID).Scan(&status, &isAdmin)
		if db.IsNoRows(err) {
			return apperr.ErrNotFound
		}
		if err != nil {
			return err
		}
		if isAdmin {
			return apperr.ErrForbidden
		}
		if status == "blocked" {
			return nil
		}
		repo := identity.Repo{}
		if err := repo.SetUserStatus(ctx, tx, userID, identity.StatusBlocked, &reason, nil); err != nil {
			return err
		}
		if err := repo.RevokeAllUserSessions(ctx, tx, userID); err != nil {
			return err
		}
		var profilePublic bool
		_ = tx.QueryRow(ctx, `SELECT profile_public FROM users WHERE id=$1`, userID).Scan(&profilePublic)
		if err := s.Registry.Upsert(ctx, tx, registry.FromUser(userID, "blocked", profilePublic)); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Input{ActorID: &subj.UserID, Action: audit.ActionAccountBlock, TargetType: "user", TargetID: userID.String(), IP: ip})
	})
}

func (s *Service) UnblockAccount(ctx context.Context, subj acl.Subject, ip netip.Addr, userID uuid.UUID) error {
	if err := s.Authz.Require(&subj, acl.PermUserBlockAccount, acl.Resource{}); err != nil {
		return err
	}
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET status='active', block_reason=NULL, updated_at=now() WHERE id=$1 AND status='blocked'`, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.Conflict("not_blocked")
		}
		var pp bool
		_ = tx.QueryRow(ctx, `SELECT profile_public FROM users WHERE id=$1`, userID).Scan(&pp)
		if err := s.Registry.Upsert(ctx, tx, registry.FromUser(userID, "active", pp)); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Input{ActorID: &subj.UserID, Action: audit.ActionAccountUnblock, TargetType: "user", TargetID: userID.String(), IP: ip})
	})
}

// ---- HTTP ----

type Handler struct{ Svc *Service }

func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(identity.RequireAuth, identity.RequireActive)
		r.Post("/reports", h.create)
		r.Get("/mod/reports", h.list)
		r.Get("/mod/reports/{id}", h.get)
		r.Post("/mod/reports/{id}/resolve", h.resolve)
		r.Post("/mod/users/{id}/block", h.block)
		r.Delete("/mod/users/{id}/block", h.unblock)
	})
}

func pid(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return id, apperr.ErrNotFound
	}
	return id, nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req CreateReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	x, created, err := h.Svc.Create(r.Context(), identity.MustSubject(r), req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	st := http.StatusOK
	if created {
		st = http.StatusCreated
	}
	httpx.JSON(w, st, x)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := pagination.ParseLimit(r.URL.Query())
	var cur pagination.Cursor
	if err == nil {
		cur, err = pagination.Decode(r.URL.Query().Get("cursor"))
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, next, err := h.Svc.List(r.Context(), identity.MustSubject(r), r.URL.Query().Get("status"), cur, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSONList(w, items, next)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pid(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	x, err := h.Svc.Get(r.Context(), identity.MustSubject(r), httpx.ClientIP(r), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, x)
}

func (h *Handler) resolve(w http.ResponseWriter, r *http.Request) {
	id, err := pid(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req struct {
		Resolution string `json:"resolution"`
		Note       string `json:"note"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.Svc.Resolve(r.Context(), identity.MustSubject(r), httpx.ClientIP(r), id, req.Resolution, req.Note); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	x, err := h.Svc.Get(r.Context(), identity.MustSubject(r), httpx.ClientIP(r), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	x.Snapshot = nil
	httpx.JSON(w, http.StatusOK, x)
}

func (h *Handler) block(w http.ResponseWriter, r *http.Request) {
	id, err := pid(r)
	var req struct {
		Reason string `json:"reason"`
	}
	if err == nil {
		err = httpx.DecodeJSON(r, &req)
	}
	if err == nil {
		err = h.Svc.BlockAccount(r.Context(), identity.MustSubject(r), httpx.ClientIP(r), id, req.Reason)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) unblock(w http.ResponseWriter, r *http.Request) {
	id, err := pid(r)
	if err == nil {
		err = h.Svc.UnblockAccount(r.Context(), identity.MustSubject(r), httpx.ClientIP(r), id)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.NoContent(w)
}
