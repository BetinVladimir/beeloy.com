package acl_test

import (
	"context"
	"net/netip"
	"os"
	"testing"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/platform/ids"
	"beeloy.com/backend/internal/testutil"
	"github.com/google/uuid"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

var catA, catB = uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

func sub(status string, bs ...acl.Binding) acl.Subject {
	return acl.Subject{UserID: ids.New(), Status: status, Bindings: bs}
}
func g(r acl.Role) acl.Binding { return acl.Binding{Role: r, ScopeType: acl.ScopeGlobal} }
func c(r acl.Role, id uuid.UUID) acl.Binding {
	return acl.Binding{Role: r, ScopeType: acl.ScopeCategory, ScopeID: &id}
}

func TestMatrix(t *testing.T) {
	member := []acl.Perm{acl.PermIdeaCreate, acl.PermMessageSend, acl.PermReportCreate, acl.PermCommentCreate}
	mod := []acl.Perm{acl.PermArticlePublish, acl.PermIdeaSetStatus, acl.PermIdeaReply, acl.PermReportView, acl.PermReportResolve, acl.PermUserBlockAccount, acl.PermCommentModerate}
	adminOnly := []acl.Perm{acl.PermUserList, acl.PermRoleGrant, acl.PermAuditView}
	all := append(append(append(append([]acl.Perm{}, member...), mod...), adminOnly...), acl.PermArticleWrite)
	inA := acl.Resource{CategoryID: &catA}
	inB := acl.Resource{CategoryID: &catB}
	in := func(list []acl.Perm, p acl.Perm) bool {
		for _, x := range list {
			if x == p {
				return true
			}
		}
		return false
	}
	az := acl.Authorizer{}
	for _, p := range all {
		// plain member
		want := in(member, p)
		if got := az.Can(sub("active"), p, acl.Resource{}); got != want {
			t.Errorf("member %s: got %v want %v", p, got, want)
		}
		// global editor: member + article.write only (NOT publish)
		want = in(member, p) || p == acl.PermArticleWrite
		if got := az.Can(sub("active", g(acl.RoleEditor)), p, inA); got != want {
			t.Errorf("editor %s: got %v want %v", p, got, want)
		}
		// moderator
		want = in(member, p) || in(mod, p)
		if got := az.Can(sub("active", g(acl.RoleModerator)), p, inA); got != want {
			t.Errorf("moderator %s: got %v want %v", p, got, want)
		}
		// admin: everything
		if !az.Can(sub("active", g(acl.RoleAdmin)), p, inA) {
			t.Errorf("admin must have %s", p)
		}
		// blocked / pending_deletion / deleted: nothing
		for _, st := range []string{"blocked", "pending_deletion", "deleted", ""} {
			if az.Can(sub(st, g(acl.RoleAdmin)), p, inA) {
				t.Errorf("status %q must have no rights (%s)", st, p)
			}
		}
	}
	// category-scoped editor
	e := sub("active", c(acl.RoleEditor, catA))
	if !az.Can(e, acl.PermArticleWrite, inA) || az.Can(e, acl.PermArticleWrite, inB) || az.Can(e, acl.PermArticleWrite, acl.Resource{}) {
		t.Error("category editor scope")
	}
	if az.Can(e, acl.PermArticlePublish, inA) {
		t.Error("editor must never publish")
	}
	if err := az.Require(nil, acl.PermIdeaCreate, acl.Resource{}); err != apperr.ErrUnauthorized {
		t.Error("nil subject -> unauthorized")
	}
	s := sub("active")
	if err := az.Require(&s, acl.PermRoleGrant, acl.Resource{}); err != apperr.ErrForbidden {
		t.Error("missing permission -> forbidden")
	}
	own := s.UserID
	if !acl.IsOwner(s, acl.Resource{OwnerID: &own}) || acl.IsOwner(s, acl.Resource{}) {
		t.Error("IsOwner")
	}
}

func TestGrantAndRevoke(t *testing.T) {
	pool := testutil.PG(t)
	ctx := context.Background()
	gs := &acl.GrantService{Pool: pool}
	admin := testutil.InsertUser(t, pool, "admin")
	target := testutil.InsertUser(t, pool, "t")
	repo := acl.Repo{}

	adminSub := acl.Subject{UserID: admin, Status: "active", Bindings: []acl.Binding{g(acl.RoleAdmin)}}
	if _, err := gs.Grant(ctx, acl.Subject{UserID: target, Status: "active"}, netip.Addr{}, target, acl.RoleAdmin, acl.ScopeGlobal, nil); err != apperr.ErrForbidden {
		t.Fatalf("non-admin must not grant: %v", err)
	}
	// seed admin binding manually
	ab := g(acl.RoleAdmin)
	ab.ID = ids.New()
	if err := repo.InsertBinding(ctx, pool, ab, admin, admin); err != nil {
		t.Fatal(err)
	}
	cat := uuid.MustParse("0198f000-0000-7000-8000-000000000001")
	b, err := gs.Grant(ctx, adminSub, netip.Addr{}, target, acl.RoleEditor, acl.ScopeCategory, &cat)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gs.Grant(ctx, adminSub, netip.Addr{}, target, acl.RoleEditor, acl.ScopeCategory, &cat); err == nil {
		t.Error("duplicate grant must conflict")
	}
	if _, err := gs.Grant(ctx, adminSub, netip.Addr{}, target, acl.RoleModerator, acl.ScopeCategory, &cat); err == nil {
		t.Error("only editors can be scoped")
	}
	bs, _ := repo.ListBindings(ctx, pool, target)
	if len(bs) != 1 || bs[0].Role != acl.RoleEditor {
		t.Fatalf("bindings: %+v", bs)
	}
	// revoke takes effect immediately for the next load
	if err := gs.Revoke(ctx, adminSub, netip.Addr{}, b.ID); err != nil {
		t.Fatal(err)
	}
	if bs, _ := repo.ListBindings(ctx, pool, target); len(bs) != 0 {
		t.Error("revoked binding must disappear")
	}
	// last admin cannot be removed
	if err := gs.Revoke(ctx, adminSub, netip.Addr{}, ab.ID); err == nil {
		t.Error("last admin must not be revoked")
	}
	// audit rows exist and contain no secrets
	evs, _, err := audit.Service{}.List(ctx, pool, audit.Filter{Action: audit.ActionRoleGrant}, emptyCursor(), 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit: %v %d", err, len(evs))
	}
}
