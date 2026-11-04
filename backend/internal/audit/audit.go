// Package audit records security-relevant actions. Events never contain secrets, e-mails or message text.
package audit

import (
	"context"
	"encoding/json"
	"net/netip"
	"time"

	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/ids"
	"beeloy.com/backend/internal/platform/pagination"
	"github.com/google/uuid"
)

const (
	ActionLogin                = "auth.login"
	ActionLogout               = "auth.logout"
	ActionSlugChange           = "user.slug_change"
	ActionRoleGrant            = "role.grant"
	ActionRoleRevoke           = "role.revoke"
	ActionAccountBlock         = "user.block"
	ActionAccountUnblock       = "user.unblock"
	ActionAccountDeleteRequest = "user.delete_request"
	ActionAccountDeleteCancel  = "user.delete_cancel"
	ActionAccountPurge         = "user.purge"
	ActionIdeaStatus           = "idea.status"
	ActionIdeaReply            = "idea.reply"
	ActionArticlePublish       = "article.publish"
	ActionArticleUnpublish     = "article.unpublish"
	ActionArticleReject        = "article.reject"
	ActionReportResolve        = "report.resolve"
	ActionDataExport           = "user.export"
	ActionI18nActivate         = "i18n.activate"
	ActionCommentHide          = "comment.hide"
	ActionCommentUnhide        = "comment.unhide"
	ActionReportViewSnapshot   = "report.view_snapshot"
)

type Event struct {
	ID         uuid.UUID      `json:"id"`
	ActorID    *uuid.UUID     `json:"actor_id"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Meta       map[string]any `json:"meta"`
	IP         string         `json:"ip,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// Input is what callers pass to Record.
type Input struct {
	ActorID    *uuid.UUID
	Action     string
	TargetType string
	TargetID   string
	Meta       map[string]any
	IP         netip.Addr
}

type Service struct{}

// Record writes the event through q (use the transaction of the audited action).
func (Service) Record(ctx context.Context, q db.DBTX, e Input) error {
	meta := e.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	mb, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	var ip *netip.Addr
	if e.IP.IsValid() {
		ip = &e.IP
	}
	_, err = q.Exec(ctx, `INSERT INTO audit_events (id, actor_id, action, target_type, target_id, meta, ip) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		ids.New(), e.ActorID, e.Action, e.TargetType, e.TargetID, mb, ip)
	return err
}

type Filter struct {
	ActorID *uuid.UUID
	Action  string
}

// List returns events newest first. Authorization (audit.view) is checked by the caller.
func (Service) List(ctx context.Context, q db.DBTX, f Filter, cur pagination.Cursor, limit int) ([]Event, string, error) {
	rows, err := q.Query(ctx, `SELECT id, actor_id, action, target_type, target_id, meta, COALESCE(host(ip),''), created_at
		FROM audit_events
		WHERE ($1::uuid IS NULL OR actor_id = $1)
		  AND ($2 = '' OR action = $2)
		  AND ($3::timestamptz IS NULL OR (created_at, id) < ($3, $4))
		ORDER BY created_at DESC, id DESC LIMIT $5`,
		f.ActorID, f.Action, nilTime(cur), cur.ID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var mb []byte
		if err := rows.Scan(&e.ID, &e.ActorID, &e.Action, &e.TargetType, &e.TargetID, &mb, &e.IP, &e.CreatedAt); err != nil {
			return nil, "", err
		}
		_ = json.Unmarshal(mb, &e.Meta)
		out = append(out, e)
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

func nilTime(c pagination.Cursor) *time.Time {
	if c.IsZero() {
		return nil
	}
	return &c.T
}
