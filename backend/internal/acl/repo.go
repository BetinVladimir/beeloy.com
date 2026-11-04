package acl

import (
	"context"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/db"
	"github.com/google/uuid"
)

type Repo struct{}

func (Repo) ListBindings(ctx context.Context, q db.DBTX, userID uuid.UUID) ([]Binding, error) {
	rows, err := q.Query(ctx, `SELECT id, role, scope_type, scope_id FROM role_bindings WHERE user_id = $1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Binding
	for rows.Next() {
		var b Binding
		if err := rows.Scan(&b.ID, &b.Role, &b.ScopeType, &b.ScopeID); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (Repo) InsertBinding(ctx context.Context, q db.DBTX, b Binding, userID, grantedBy uuid.UUID) error {
	_, err := q.Exec(ctx, `INSERT INTO role_bindings (id, user_id, role, scope_type, scope_id, granted_by) VALUES ($1,$2,$3,$4,$5,$6)`,
		b.ID, userID, b.Role, b.ScopeType, b.ScopeID, grantedBy)
	if db.IsUniqueViolation(err, "") {
		return apperr.Conflict("already_granted")
	}
	if db.IsForeignKeyViolation(err) {
		return apperr.ErrNotFound
	}
	return err
}

// DeleteBinding removes a binding and returns it with its user id.
func (Repo) DeleteBinding(ctx context.Context, q db.DBTX, id uuid.UUID) (Binding, uuid.UUID, error) {
	var b Binding
	var uid uuid.UUID
	err := q.QueryRow(ctx, `DELETE FROM role_bindings WHERE id = $1 RETURNING id, role, scope_type, scope_id, user_id`, id).
		Scan(&b.ID, &b.Role, &b.ScopeType, &b.ScopeID, &uid)
	if db.IsNoRows(err) {
		return b, uid, apperr.ErrNotFound
	}
	return b, uid, err
}

func (Repo) GetBinding(ctx context.Context, q db.DBTX, id uuid.UUID) (Binding, uuid.UUID, error) {
	var b Binding
	var uid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id, role, scope_type, scope_id, user_id FROM role_bindings WHERE id = $1`, id).
		Scan(&b.ID, &b.Role, &b.ScopeType, &b.ScopeID, &uid)
	if db.IsNoRows(err) {
		return b, uid, apperr.ErrNotFound
	}
	return b, uid, err
}

// CountGlobalAdmins counts bindings admin/global of active users.
func (Repo) CountGlobalAdmins(ctx context.Context, q db.DBTX) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM role_bindings rb JOIN users u ON u.id = rb.user_id
		WHERE rb.role = 'admin' AND rb.scope_type = 'global' AND u.status = 'active'`).Scan(&n)
	return n, err
}

// LockAdmins serializes every flow that can remove or deactivate a global admin (ТЗ lock 7007).
func LockAdmins(ctx context.Context, q db.DBTX) error {
	_, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(7007)`)
	return err
}

// GuardLastAdmin must run inside a tx after LockAdmins: it fails with conflict "last_admin" when userID is
// the only active global admin. The count is taken after the lock, so concurrent flows cannot both pass.
func GuardLastAdmin(ctx context.Context, q db.DBTX, userID uuid.UUID) error {
	var isAdmin bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM role_bindings rb JOIN users u ON u.id = rb.user_id
		WHERE rb.user_id = $1 AND rb.role = 'admin' AND rb.scope_type = 'global' AND u.status = 'active')`, userID).Scan(&isAdmin); err != nil || !isAdmin {
		return err
	}
	n, err := Repo{}.CountGlobalAdmins(ctx, q)
	if err != nil {
		return err
	}
	if n <= 1 {
		return apperr.Conflict("last_admin")
	}
	return nil
}
