// Package acl implements roles, permissions and the server-side authorizer.
package acl

import (
	"context"

	"github.com/google/uuid"
)

type Role string

const (
	RoleEditor    Role = "editor"
	RoleModerator Role = "moderator"
	RoleAdmin     Role = "admin"
)

type ScopeType string

const (
	ScopeGlobal   ScopeType = "global"
	ScopeCategory ScopeType = "category"
)

// Binding is one role granted to a user, optionally limited to a category (editor only).
type Binding struct {
	ID        uuid.UUID  `json:"id"`
	Role      Role       `json:"role"`
	ScopeType ScopeType  `json:"scope_type"`
	ScopeID   *uuid.UUID `json:"scope_id"`
}

type Perm string

const (
	PermIdeaCreate       Perm = "idea.create"
	PermIdeaSetStatus    Perm = "idea.set_status"
	PermIdeaReply        Perm = "idea.reply"
	PermMessageSend      Perm = "message.send"
	PermReportCreate     Perm = "report.create"
	PermReportView       Perm = "report.view"
	PermReportResolve    Perm = "report.resolve"
	PermArticleWrite     Perm = "article.write"
	PermArticlePublish   Perm = "article.publish"
	PermUserBlockAccount Perm = "user.block_account"
	PermUserList         Perm = "user.list"
	PermRoleGrant        Perm = "role.grant"
	PermAuditView        Perm = "audit.view"
	PermCommentCreate    Perm = "comment.create"
	PermCommentModerate  Perm = "comment.moderate"
)

// Subject is the authenticated caller. Bindings are loaded from the database on every request.
type Subject struct {
	UserID   uuid.UUID
	Status   string // users.status
	Bindings []Binding
}

// Resource describes the object a permission is checked against.
type Resource struct {
	Type       string
	OwnerID    *uuid.UUID
	CategoryID *uuid.UUID
}

type ctxKey struct{}

func WithSubject(ctx context.Context, s Subject) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

func SubjectFrom(ctx context.Context) (Subject, bool) {
	s, ok := ctx.Value(ctxKey{}).(Subject)
	return s, ok
}

// HasRole reports whether the subject holds role r in any scope.
func (s Subject) HasRole(r Role) bool {
	for _, b := range s.Bindings {
		if b.Role == r {
			return true
		}
	}
	return false
}
