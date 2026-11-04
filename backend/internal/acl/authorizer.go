package acl

import "beeloy.com/backend/internal/apperr"

var memberPerms = []Perm{PermIdeaCreate, PermMessageSend, PermReportCreate, PermCommentCreate}

var rolePerms = map[Role][]Perm{
	RoleEditor: {PermArticleWrite},
	RoleModerator: {PermArticlePublish, PermIdeaSetStatus, PermIdeaReply, PermReportView, PermReportResolve,
		PermUserBlockAccount, PermCommentModerate},
	RoleAdmin: {PermArticleWrite, PermArticlePublish, PermIdeaSetStatus, PermIdeaReply, PermReportView, PermReportResolve,
		PermUserBlockAccount, PermCommentModerate, PermUserList, PermRoleGrant, PermAuditView},
}

func has(list []Perm, p Perm) bool {
	for _, x := range list {
		if x == p {
			return true
		}
	}
	return false
}

type Authorizer struct{}

// Can implements default-deny: only active users have rights; members get the member set;
// role bindings add permissions limited by scope.
func (Authorizer) Can(s Subject, p Perm, res Resource) bool {
	if s.Status != "active" {
		return false
	}
	if has(memberPerms, p) {
		return true
	}
	for _, b := range s.Bindings {
		if !has(rolePerms[b.Role], p) {
			continue
		}
		switch b.ScopeType {
		case ScopeGlobal:
			return true
		case ScopeCategory:
			if res.CategoryID != nil && b.ScopeID != nil && *res.CategoryID == *b.ScopeID {
				return true
			}
		}
	}
	return false
}

// Require returns ErrUnauthorized for a missing subject and ErrForbidden when the permission is absent.
func (a Authorizer) Require(s *Subject, p Perm, res Resource) error {
	if s == nil {
		return apperr.ErrUnauthorized
	}
	if !a.Can(*s, p, res) {
		return apperr.ErrForbidden
	}
	return nil
}

// IsOwner reports whether the subject owns the resource.
func IsOwner(s Subject, res Resource) bool { return res.OwnerID != nil && *res.OwnerID == s.UserID }
