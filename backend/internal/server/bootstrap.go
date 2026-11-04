package server

import (
	"context"
	"fmt"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/ids"
	"github.com/jackc/pgx/v5"
)

// BootstrapAdmin creates the first administrator for email (the user signs in with the usual e-mail code).
// It refuses to run when a global admin already exists.
func (s *Server) BootstrapAdmin(ctx context.Context, email string) error {
	email, err := identity.NormalizeEmail(email)
	if err != nil {
		return err
	}
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7007)`); err != nil {
			return err
		}
		repo, arepo := identity.Repo{}, acl.Repo{}
		n, err := arepo.CountGlobalAdmins(ctx, tx)
		if err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("an administrator already exists")
		}
		var userID = ids.New()
		ident, err := repo.FindIdentity(ctx, tx, identity.ProviderEmailOTP, email)
		switch {
		case err == nil:
			userID = ident.UserID
		default:
			nick := "User" // public name never derives from the e-mail
			slug, err := s.Slugs.GenerateForUser(ctx, tx, nick, false)
			if err != nil {
				return err
			}
			u := identity.User{ID: userID, Nickname: nick, Locale: "bg", Status: identity.StatusActive, ProfilePublic: true}
			if err := repo.CreateUser(ctx, tx, u); err != nil {
				return err
			}
			if err := repo.InsertIdentity(ctx, tx, identity.AuthIdentity{ID: ids.New(), UserID: userID, Provider: identity.ProviderEmailOTP, Subject: email, Email: &email, EmailVerified: true}); err != nil {
				return err
			}
			if err := s.Slugs.Assign(ctx, tx, identity.EntityUser, userID, slug); err != nil {
				return err
			}
			if s.AfterUserCreate != nil {
				u.Slug = slug
				if err := s.AfterUserCreate(ctx, tx, u); err != nil {
					return err
				}
			}
		}
		return arepo.InsertBinding(ctx, tx, acl.Binding{ID: ids.New(), Role: acl.RoleAdmin, ScopeType: acl.ScopeGlobal}, userID, userID)
	})
}

func indexAt(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '@' {
			return i
		}
	}
	return len(s)
}
