package admin_test

import (
	"os"
	"strings"
	"testing"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/server/servertest"
	"beeloy.com/backend/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

func TestRolesUsersAndAudit(t *testing.T) {
	env := servertest.New(t)
	if err := env.Server.BootstrapAdmin(t.Context(), "root@example.com"); err != nil {
		t.Fatal(err)
	}
	root := env.LoginAs("root@example.com")
	member := env.LoginAs("member@example.com")
	memID := member.UserID()
	anon := env.Client()

	for name, c := range map[string]*servertest.Client{"member": member} {
		if c.Get("/api/v1/admin/users").Code != 403 || c.Get("/api/v1/admin/audit").Code != 403 || c.Post("/api/v1/admin/role-bindings", map[string]any{"user_id": memID, "role": "admin"}).Code != 403 {
			t.Errorf("%s must not use admin endpoints", name)
		}
	}
	if anon.Get("/api/v1/admin/users").Code != 401 {
		t.Error("anonymous")
	}
	// list has no e-mails and supports search
	list := root.Get("/api/v1/admin/users?q=member")
	if list.Code != 200 || len(list.List()) != 1 || strings.Contains(string(list.Raw), "@example.com") || strings.Contains(string(list.Raw), "email") {
		t.Fatalf("users: %d %s", list.Code, list.Raw)
	}
	// grant -> effective on the very next request; revoke -> effective at once
	g := root.Post("/api/v1/admin/role-bindings", map[string]any{"user_id": memID, "role": "moderator"})
	if g.Code != 201 {
		t.Fatalf("grant: %d %s", g.Code, g.Raw)
	}
	if member.Get("/api/v1/mod/reports").Code != 200 {
		t.Error("granted role works immediately")
	}
	if root.Post("/api/v1/admin/role-bindings", map[string]any{"user_id": memID, "role": "moderator"}).Code != 409 {
		t.Error("duplicate grant")
	}
	if root.Post("/api/v1/admin/role-bindings", map[string]any{"user_id": memID, "role": "king"}).Code != 422 {
		t.Error("unknown role")
	}
	if root.Post("/api/v1/admin/role-bindings", map[string]any{"user_id": memID, "role": "editor", "scope_type": "category", "scope_id": servertest.CatNews}).Code != 201 {
		t.Error("category editor")
	}
	if root.Delete("/api/v1/admin/role-bindings/"+g.Data()["id"].(string)).Code != 204 {
		t.Fatal("revoke")
	}
	if member.Get("/api/v1/mod/reports").Code != 403 {
		t.Error("revoked role stops working immediately")
	}
	// the last admin stays
	me := root.Get("/api/v1/me").Data()["roles"].([]any)[0].(map[string]any)
	var adminBinding string
	env.Server.Pool.QueryRow(t.Context(), `SELECT id FROM role_bindings WHERE role='admin'`).Scan(&adminBinding)
	_ = me
	if r := root.Delete("/api/v1/admin/role-bindings/" + adminBinding); r.Code != 409 {
		t.Errorf("last admin: %d %s", r.Code, r.Raw)
	}
	// audit log
	a := root.Get("/api/v1/admin/audit?action=role.grant")
	if a.Code != 200 || len(a.List()) != 2 {
		t.Fatalf("audit: %d %s", a.Code, a.Raw)
	}
	if strings.Contains(string(a.Raw), "example.com") {
		t.Error("audit must not contain e-mails")
	}
	_ = acl.RoleAdmin
}
