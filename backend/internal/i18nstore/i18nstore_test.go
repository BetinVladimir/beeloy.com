package i18nstore_test

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

func TestFallbackWhenNothingActive(t *testing.T) {
	env := servertest.New(t)
	r := env.Client().Get("/api/v1/i18n/en")
	if r.Code != 200 || r.Header.Get("X-I18n-Fallback") != "1" || r.JSON["login.title"] != "Sign in" {
		t.Fatalf("fallback: %d %v %s", r.Code, r.Header, r.Raw)
	}
	if _, wrapped := r.JSON["data"]; wrapped {
		t.Error("bundle must not be wrapped in data")
	}
	if env.Client().Get("/api/v1/i18n/tr").Code != 404 {
		t.Error("unsupported locale must be 404")
	}
	m := env.Client().Get("/api/v1/i18n/meta")
	if m.Code != 200 || m.Data()["default_locale"] != "bg" || len(m.Data()["locales"].([]any)) != 26 {
		t.Fatalf("meta: %s", m.Raw)
	}
}

func TestActiveReleaseETagAnd304(t *testing.T) {
	env := servertest.New(t)
	ctx := context.Background()
	p := env.Server.Pool
	if _, err := p.Exec(ctx, `INSERT INTO i18n_releases (version, manifest, status) VALUES ('v1','{}','active')`); err != nil {
		t.Fatal(err)
	}
	p.Exec(ctx, `INSERT INTO i18n_bundles (version, locale, content, sha256) VALUES ('v1','bg','{"hello":"Здравей"}','abc123')`)
	p.Exec(ctx, `INSERT INTO i18n_active (id, version) VALUES (1,'v1')`)
	env.Server.I18n.Invalidate()
	c := env.Client()
	r := c.Get("/api/v1/i18n/bg")
	if r.Code != 200 || r.JSON["hello"] != "Здравей" || r.Header.Get("ETag") != `"abc123"` || r.Header.Get("X-I18n-Fallback") != "" {
		t.Fatalf("active: %d %v %s", r.Code, r.Header, r.Raw)
	}
	// second call comes from Redis and is identical
	if r2 := c.Get("/api/v1/i18n/bg"); string(r2.Raw) != string(r.Raw) {
		t.Error("cached response differs")
	}
	// conditional request
	c.Headers = map[string]string{"If-None-Match": `"abc123"`}
	if r := c.Get("/api/v1/i18n/bg"); r.Code != 304 || len(r.Raw) != 0 {
		t.Fatalf("conditional request: %d", r.Code)
	}
	c.Headers = map[string]string{"If-None-Match": `"other"`}
	if r := c.Get("/api/v1/i18n/bg"); r.Code != 200 {
		t.Fatalf("stale etag: %d", r.Code)
	}
	c.Headers = nil
	if c.Get("/api/v1/i18n/ru").Code != 404 {
		t.Error("locale missing in the release is 404")
	}
	// Redis outage does not break reading
	_ = env.Server.Redis.Close()
	if r := c.Get("/api/v1/i18n/bg"); r.Code != 200 {
		t.Errorf("fail-open on Redis outage: %d", r.Code)
	}
}
