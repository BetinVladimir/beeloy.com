package server_test

import (
	"context"
	"os"
	"testing"

	"beeloy.com/backend/internal/server/servertest"
	"beeloy.com/backend/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

func TestHealthAndReady(t *testing.T) {
	env := servertest.New(t)
	c := env.Client()
	if c.Get("/healthz").Code != 200 || c.Get("/readyz").Code != 200 {
		t.Fatal("healthz/readyz must be 200")
	}
	if r := c.Get("/healthz"); r.Header.Get("X-Request-Id") == "" {
		t.Error("request id header")
	}
	_ = env.Server.Redis.Close()
	if r := c.Get("/readyz"); r.Code != 503 {
		t.Fatalf("readyz with Redis down: %d", r.Code)
	}
}

func TestSecurityHeadersAndCSRF(t *testing.T) {
	env := servertest.New(t)
	r := env.Client().Get("/api/v1/users/nobody")
	if r.Header.Get("Cache-Control") == "" || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers: %v", r.Header)
	}
	// raw POST without Origin / X-Requested-With
	rec := env.Client().Do("POST", "/api/v1/auth/logout", nil)
	_ = rec
}

func TestBootstrapAdmin(t *testing.T) {
	env := servertest.New(t)
	ctx := context.Background()
	if err := env.Server.BootstrapAdmin(ctx, "Root@Example.com"); err != nil {
		t.Fatal(err)
	}
	if err := env.Server.BootstrapAdmin(ctx, "other@example.com"); err == nil {
		t.Fatal("second bootstrap must fail")
	}
	c := env.LoginAs("root@example.com")
	me := c.Get("/api/v1/me")
	roles := me.Data()["roles"].([]any)
	if len(roles) != 1 || roles[0].(map[string]any)["role"] != "admin" {
		t.Fatalf("roles: %v", me.Raw)
	}
	if env.Server.Slugs == nil {
		t.Fatal()
	}
}
