// Package privacy implements data export, account deletion (with a 30-day grace period) and the final purge.
package privacy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"time"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/httpx"
	"beeloy.com/backend/internal/platform/outbox"
	"beeloy.com/backend/internal/platform/ratelimit"
	"beeloy.com/backend/internal/registry"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const GracePeriod = 30 * 24 * time.Hour

var ruleExport = ratelimit.Rule{Name: "data_export", Limit: 3, Window: 24 * time.Hour}

type Service struct {
	Pool     *pgxpool.Pool
	Users    identity.Repo
	Audit    audit.Service
	Lim      *ratelimit.Limiter
	Registry registry.Repo
	Kick     func()
	// Journal (optional in dev, required in prod) records purges outside PostgreSQL for replay after a restore.
	Journal Journal
}

func (s *Service) profileVisibility(ctx context.Context, q db.DBTX, id uuid.UUID) bool {
	var pp bool
	_ = q.QueryRow(ctx, `SELECT profile_public FROM users WHERE id=$1`, id).Scan(&pp)
	return pp
}

// RequestDeletion moves an active account to pending_deletion (idempotent). The data is purged after the grace period.
func (s *Service) RequestDeletion(ctx context.Context, subj acl.Subject, ip netip.Addr) error {
	if subj.Status == string(identity.StatusPendingDeletion) {
		return nil
	}
	if subj.Status != string(identity.StatusActive) {
		return apperr.ErrForbidden
	}
	err := db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := acl.LockAdmins(ctx, tx); err != nil {
			return err
		}
		if err := acl.GuardLastAdmin(ctx, tx, subj.UserID); err != nil {
			return err
		}
		now := time.Now()
		if err := s.Users.SetUserStatus(ctx, tx, subj.UserID, identity.StatusPendingDeletion, nil, &now); err != nil {
			return err
		}
		if err := s.Registry.Upsert(ctx, tx, registry.FromUser(subj.UserID, "pending_deletion", s.profileVisibility(ctx, tx, subj.UserID))); err != nil {
			return err
		}
		if err := outbox.Add(ctx, tx, outbox.Event{Type: "user.deletion_requested", AggregateType: "user", AggregateID: subj.UserID}); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Input{ActorID: &subj.UserID, Action: audit.ActionAccountDeleteRequest, TargetType: "user", TargetID: subj.UserID.String(), IP: ip})
	})
	if err == nil && s.Kick != nil {
		s.Kick()
	}
	return err
}

// CancelDeletion restores an account that is waiting for deletion.
func (s *Service) CancelDeletion(ctx context.Context, subj acl.Subject, ip netip.Addr) error {
	if subj.Status != string(identity.StatusPendingDeletion) {
		return apperr.Conflict("not_pending_deletion")
	}
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.Users.SetUserStatus(ctx, tx, subj.UserID, identity.StatusActive, nil, nil); err != nil {
			return err
		}
		if err := s.Registry.Upsert(ctx, tx, registry.FromUser(subj.UserID, "active", s.profileVisibility(ctx, tx, subj.UserID))); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Input{ActorID: &subj.UserID, Action: audit.ActionAccountDeleteCancel, TargetType: "user", TargetID: subj.UserID.String(), IP: ip})
	})
}

type ExportDoc struct {
	GeneratedAt   time.Time        `json:"generated_at"`
	Profile       map[string]any   `json:"profile"`
	Slugs         []string         `json:"slugs"`
	Identities    []map[string]any `json:"identities"`
	Agreements    []map[string]any `json:"agreements"`
	Ideas         []map[string]any `json:"ideas"`
	Votes         []map[string]any `json:"votes"`
	Comments      []map[string]any `json:"comments"`
	Conversations []map[string]any `json:"conversations"`
	Reports       []map[string]any `json:"reports"`
}

func collect(ctx context.Context, q db.DBTX, sql string, args ...any) ([]map[string]any, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, f := range rows.FieldDescriptions() {
			m[f.Name] = normalize(vals[i])
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func normalize(v any) any {
	if b, ok := v.([16]byte); ok {
		return uuid.UUID(b).String()
	}
	return v
}

// Export returns everything the platform holds about the user (never other people's data).
func (s *Service) Export(ctx context.Context, subj acl.Subject, ip netip.Addr) (ExportDoc, error) {
	if subj.Status != string(identity.StatusActive) && subj.Status != string(identity.StatusPendingDeletion) {
		return ExportDoc{}, apperr.ErrForbidden
	}
	if err := s.Lim.Check(ctx, ruleExport, subj.UserID.String(), false); err != nil {
		return ExportDoc{}, err
	}
	u, err := s.Users.GetUserByID(ctx, s.Pool, subj.UserID)
	if err != nil {
		return ExportDoc{}, err
	}
	d := ExportDoc{GeneratedAt: time.Now().UTC(), Slugs: []string{},
		Profile: map[string]any{"id": u.ID, "nickname": u.Nickname, "bio": u.Bio, "locale": u.Locale, "status": u.Status, "profile_public": u.ProfilePublic, "slug": u.Slug, "created_at": u.CreatedAt}}
	rows, err := s.Pool.Query(ctx, `SELECT slug FROM slugs WHERE entity_type='user' AND entity_id=$1 ORDER BY created_at`, subj.UserID)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var sl string
		_ = rows.Scan(&sl)
		d.Slugs = append(d.Slugs, sl)
	}
	rows.Close()
	steps := []struct {
		dst  *[]map[string]any
		sql  string
		args []any
	}{
		{&d.Identities, `SELECT provider, email, created_at FROM auth_identities WHERE user_id=$1`, nil},
		{&d.Agreements, `SELECT doc, version, accepted_at FROM user_agreements WHERE user_id=$1`, nil},
		{&d.Ideas, `SELECT i.title, i.body, i.category, i.status, i.votes_count, i.created_at FROM ideas i WHERE i.author_id=$1 ORDER BY i.created_at`, nil},
		{&d.Votes, `SELECT COALESCE(s.slug,'') AS idea_slug, v.created_at FROM idea_votes v LEFT JOIN slugs s ON s.entity_type='idea' AND s.entity_id=v.idea_id AND s.is_current WHERE v.user_id=$1`, nil},
		{&d.Comments, `SELECT target_type, body, status, created_at FROM comments WHERE author_id=$1 ORDER BY created_at`, nil},
		{&d.Reports, `SELECT target_type, reason, details, status, created_at FROM reports WHERE reporter_id=$1 ORDER BY created_at`, nil},
	}
	for _, st := range steps {
		if *st.dst, err = collect(ctx, s.Pool, st.sql, subj.UserID); err != nil {
			return d, err
		}
	}
	convs, err := s.Pool.Query(ctx, `SELECT c.id, COALESCE(ps.slug,'') FROM conversations c
		JOIN users p ON p.id = CASE WHEN c.user_low=$1 THEN c.user_high ELSE c.user_low END
		LEFT JOIN slugs ps ON ps.entity_type='user' AND ps.entity_id=p.id AND ps.is_current
		WHERE c.user_low=$1 OR c.user_high=$1 ORDER BY c.created_at`, subj.UserID)
	if err != nil {
		return d, err
	}
	type cv struct {
		id   uuid.UUID
		peer string
	}
	var cvs []cv
	for convs.Next() {
		var x cv
		_ = convs.Scan(&x.id, &x.peer)
		cvs = append(cvs, x)
	}
	convs.Close()
	d.Conversations = []map[string]any{}
	for _, c := range cvs {
		msgs, err := collect(ctx, s.Pool, `SELECT seq, (sender_id = $2) AS mine, CASE WHEN deleted_at IS NULL THEN body ELSE '' END AS body, created_at
			FROM messages WHERE conversation_id=$1 ORDER BY seq`, c.id, subj.UserID)
		if err != nil {
			return d, err
		}
		d.Conversations = append(d.Conversations, map[string]any{"peer_slug": c.peer, "messages": msgs})
	}
	if err := s.Audit.Record(ctx, s.Pool, audit.Input{ActorID: &subj.UserID, Action: audit.ActionDataExport, TargetType: "user", TargetID: subj.UserID.String(), IP: ip}); err != nil {
		return d, err
	}
	return d, nil
}

// PurgeUser irreversibly anonymizes an account (called by the job after the grace period).
func (s *Service) PurgeUser(ctx context.Context, userID uuid.UUID) error {
	return s.purge(ctx, userID, true)
}

// purge runs the anonymization in one tx. With journal=true the deletion is journaled as the last step before commit:
// a journal failure rolls the purge back, and only a failed commit can leave a spurious entry (the purge is then retried).
func (s *Service) purge(ctx context.Context, userID uuid.UUID, journal bool) error {
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := acl.LockAdmins(ctx, tx); err != nil {
			return err
		}
		if err := acl.GuardLastAdmin(ctx, tx, userID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE users SET nickname='Deleted user', bio='', avatar_url=NULL, profile_public=false, status='deleted', locale='bg',
			block_reason=NULL, deletion_requested_at=NULL, updated_at=now() WHERE id=$1 AND status <> 'deleted'`, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		stmts := []string{
			`DELETE FROM auth_identities WHERE user_id=$1`,
			`DELETE FROM sessions WHERE user_id=$1`,
			`DELETE FROM slugs WHERE entity_type='user' AND entity_id=$1`,
			`UPDATE messages SET body='', deleted_at=now() WHERE sender_id=$1 AND deleted_at IS NULL`,
			`DELETE FROM user_blocks WHERE blocker_id=$1 OR blocked_id=$1`,
			`UPDATE ideas SET votes_count = GREATEST(votes_count - v.n, 0) FROM (SELECT idea_id, count(*) AS n FROM idea_votes WHERE user_id=$1 GROUP BY idea_id) v WHERE ideas.id = v.idea_id`,
			`DELETE FROM idea_votes WHERE user_id=$1`,
			`UPDATE ideas SET author_id=NULL WHERE author_id=$1`,
			`UPDATE articles SET author_id=NULL WHERE author_id=$1`,
			`UPDATE reports SET reporter_id=NULL WHERE reporter_id=$1`,
			`UPDATE comments SET body='', status='deleted', author_id=NULL WHERE author_id=$1`,
			`DELETE FROM user_agreements WHERE user_id=$1`,
			`UPDATE audit_events SET actor_id=NULL, ip=NULL WHERE actor_id=$1`,
			`DELETE FROM role_bindings WHERE user_id=$1`,
			`DELETE FROM article_access WHERE user_id=$1`,
		}
		for _, q := range stmts {
			if _, err := tx.Exec(ctx, q, userID); err != nil {
				return err
			}
		}
		if err := s.Registry.Upsert(ctx, tx, registry.FromUser(userID, "deleted", false)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE resources SET owner_id=NULL, status = CASE WHEN type='comment' THEN 'removed' ELSE status END WHERE owner_id=$1 AND id <> $1`, userID); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, tx, audit.Input{Action: audit.ActionAccountPurge, TargetType: "user", TargetID: userID.String()}); err != nil {
			return err
		}
		if journal && s.Journal != nil {
			return s.Journal.Record(ctx, userID, time.Now())
		}
		return nil
	})
}

// DueForPurge returns up to n accounts whose grace period is over.
func (s *Service) DueForPurge(ctx context.Context, n int) ([]uuid.UUID, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id FROM users WHERE status='pending_deletion' AND deletion_requested_at < $1 ORDER BY deletion_requested_at LIMIT $2`, time.Now().Add(-GracePeriod), n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---- HTTP ----

type Handler struct{ Svc *Service }

func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(identity.RequireAuth) // pending_deletion accounts may use these
		r.Delete("/me", h.deleteMe)
		r.Post("/me/restore", h.restore)
		r.Get("/me/export", h.export)
	})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request, status int) {
	subj := identity.MustSubject(r)
	u, err := h.Svc.Users.GetUserByID(r.Context(), h.Svc.Pool, subj.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, status, identity.BuildMe(u, subj.Bindings))
}

func (h *Handler) deleteMe(w http.ResponseWriter, r *http.Request) {
	if err := h.Svc.RequestDeletion(r.Context(), identity.MustSubject(r), httpx.ClientIP(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.me(w, r, http.StatusAccepted)
}

func (h *Handler) restore(w http.ResponseWriter, r *http.Request) {
	if err := h.Svc.CancelDeletion(r.Context(), identity.MustSubject(r), httpx.ClientIP(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.me(w, r, http.StatusOK)
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	d, err := h.Svc.Export(r.Context(), identity.MustSubject(r), httpx.ClientIP(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="beeloy-export.json"`)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(d)
}
