package acl

import (
	"context"
	"net/netip"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/ids"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type GrantService struct {
	Pool  *pgxpool.Pool
	Repo  Repo
	Audit audit.Service
	Authz Authorizer
}

// Grant gives role to a user. Only editors may be limited to a category; the category must exist.
func (g *GrantService) Grant(ctx context.Context, actor Subject, ip netip.Addr, target uuid.UUID, role Role, st ScopeType, scopeID *uuid.UUID) (Binding, error) {
	if err := g.Authz.Require(&actor, PermRoleGrant, Resource{}); err != nil {
		return Binding{}, err
	}
	switch role {
	case RoleEditor, RoleModerator, RoleAdmin:
	default:
		return Binding{}, apperr.Field("role", "invalid_value")
	}
	if st == "" {
		st = ScopeGlobal
	}
	switch st {
	case ScopeGlobal:
		if scopeID != nil {
			return Binding{}, apperr.Field("scope_id", "invalid_value")
		}
	case ScopeCategory:
		if role != RoleEditor {
			return Binding{}, apperr.Field("scope_type", "invalid_value")
		}
		if scopeID == nil {
			return Binding{}, apperr.Field("scope_id", "required")
		}
	default:
		return Binding{}, apperr.Field("scope_type", "invalid_value")
	}
	b := Binding{ID: ids.New(), Role: role, ScopeType: st, ScopeID: scopeID}
	err := db.WithTx(ctx, g.Pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1 AND status = 'active')`, target).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return apperr.ErrNotFound
		}
		if scopeID != nil {
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM categories WHERE id = $1)`, *scopeID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return apperr.Field("scope_id", "invalid_value")
			}
		}
		if err := g.Repo.InsertBinding(ctx, tx, b, target, actor.UserID); err != nil {
			return err
		}
		return g.Audit.Record(ctx, tx, audit.Input{ActorID: &actor.UserID, Action: audit.ActionRoleGrant, TargetType: "user", TargetID: target.String(),
			Meta: map[string]any{"role": role, "scope_type": st}, IP: ip})
	})
	return b, err
}

// Revoke removes a binding; the last global admin cannot be removed.
func (g *GrantService) Revoke(ctx context.Context, actor Subject, ip netip.Addr, bindingID uuid.UUID) error {
	if err := g.Authz.Require(&actor, PermRoleGrant, Resource{}); err != nil {
		return err
	}
	return db.WithTx(ctx, g.Pool, func(tx pgx.Tx) error {
		b, uid, err := g.Repo.GetBinding(ctx, tx, bindingID)
		if err != nil {
			return err
		}
		if b.Role == RoleAdmin && b.ScopeType == ScopeGlobal {
			if err := LockAdmins(ctx, tx); err != nil {
				return err
			}
			if err := GuardLastAdmin(ctx, tx, uid); err != nil {
				return err
			}
		}
		if _, _, err := g.Repo.DeleteBinding(ctx, tx, bindingID); err != nil {
			return err
		}
		return g.Audit.Record(ctx, tx, audit.Input{ActorID: &actor.UserID, Action: audit.ActionRoleRevoke, TargetType: "user", TargetID: uid.String(),
			Meta: map[string]any{"role": b.Role, "scope_type": b.ScopeType}, IP: ip})
	})
}
