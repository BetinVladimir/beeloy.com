package identity_test

import (
	"context"
	"net/netip"
	"os"
	"strings"
	"testing"

	"beeloy.com/backend/internal/config"
	"beeloy.com/backend/internal/server/servertest"
	"beeloy.com/backend/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

func TestOTPLoginRegisterFlow(t *testing.T) {
	env := servertest.New(t)
	c := env.Client()
	r := env.SignIn(c, "  Alice@Example.com ", nil)
	if r.Code != 200 || r.Data()["is_new"] != true {
		t.Fatalf("first sign-in: %d %s", r.Code, r.Raw)
	}
	u := r.Data()["user"].(map[string]any)
	if u["nickname"] != "User" || !strings.HasPrefix(u["slug"].(string), "user-") || strings.Contains(u["slug"].(string), "alice") || u["status"] != "active" || u["locale"] != "en" {
		t.Fatalf("new user: %v", u)
	}
	if strings.Contains(string(r.Raw), "example.com") {
		t.Error("e-mail must never be returned")
	}
	if me := c.Get("/api/v1/me"); me.Code != 200 || me.Data()["id"] != u["id"] {
		t.Fatalf("me: %d %s", me.Code, me.Raw)
	}
	// agreements recorded
	var n int
	if err := env.Server.Pool.QueryRow(context.Background(), `SELECT count(*) FROM user_agreements`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("agreements: %d %v", n, err)
	}
	// same user on the second sign-in, other case/spaces, no terms needed any more
	c2 := env.Client()
	r2 := env.SignIn(c2, "ALICE@example.com", map[string]any{"accept_terms": false})
	if r2.Code != 200 || r2.Data()["is_new"] != false || r2.Data()["user"].(map[string]any)["id"] != u["id"] {
		t.Fatalf("second sign-in: %d %s", r2.Code, r2.Raw)
	}
	// logout revokes the session on the server side
	if r := c.Post("/api/v1/auth/logout", nil); r.Code != 204 {
		t.Fatal(r.Code)
	}
	stale := env.Client()
	stale.Cookies["bl_session"] = "garbage"
	if stale.Get("/api/v1/me").Code != 401 {
		t.Error("garbage cookie must be 401")
	}
}

func TestTermsRequiredForNewAccountOnly(t *testing.T) {
	env := servertest.New(t)
	c := env.Client()
	r := env.SignIn(c, "bob@example.com", map[string]any{"accept_terms": false})
	if r.Code != 422 || r.JSON["error"].(map[string]any)["fields"].(map[string]any)["accept_terms"] != "required" {
		t.Fatalf("want 422 accept_terms: %d %s", r.Code, r.Raw)
	}
	var n int
	_ = env.Server.Pool.QueryRow(context.Background(), `SELECT count(*) FROM users`).Scan(&n)
	if n != 0 {
		t.Error("no user may be created without terms")
	}
}

func TestOTPWrongCodesBurnTheCode(t *testing.T) {
	env := servertest.New(t)
	c := env.Client()
	c.Post("/api/v1/auth/otp/request", map[string]any{"email": "carol@example.com"})
	good := ""
	for _, w := range strings.Fields(env.Mailer.Messages()[0].TextBody) {
		if len(w) == 6 && strings.Trim(w, "0123456789") == "" {
			good = w
		}
	}
	bad := "000000"
	if good == bad {
		bad = "111111"
	}
	for i := 0; i < 5; i++ {
		c.WithIP("203.0.113." + string(rune('1'+i)))
		if r := c.Post("/api/v1/auth/otp/verify", map[string]any{"email": "carol@example.com", "code": bad, "accept_terms": true}); r.ErrCode() != "invalid_code" {
			t.Fatalf("attempt %d: %d %s", i, r.Code, r.Raw)
		}
	}
	// the right code no longer works: attempts exhausted
	if r := c.Post("/api/v1/auth/otp/verify", map[string]any{"email": "carol@example.com", "code": good, "accept_terms": true}); r.ErrCode() != "invalid_code" {
		t.Fatalf("burned code must fail: %d %s", r.Code, r.Raw)
	}
}

func TestOTPCooldownAndNoEnumeration(t *testing.T) {
	env := servertest.New(t)
	c := env.Client()
	env.SignIn(c, "dave@example.com", nil) // existing user now
	r1 := c.Post("/api/v1/auth/otp/request", map[string]any{"email": "dave@example.com"})
	n := len(env.Mailer.Messages())
	r2 := c.Post("/api/v1/auth/otp/request", map[string]any{"email": "dave@example.com"}) // within 60s: no mail
	r3 := c.Post("/api/v1/auth/otp/request", map[string]any{"email": "unknown@example.com"})
	if r1.Code != 202 || r2.Code != 202 || r3.Code != 202 {
		t.Fatalf("all must be 202: %d %d %d", r1.Code, r2.Code, r3.Code)
	}
	if len(env.Mailer.Messages()) != n+1 { // r2 sends nothing, r3 sends one
		t.Fatalf("mails: %d (before %d)", len(env.Mailer.Messages()), n)
	}
	if string(r1.Raw) != string(r2.Raw) || string(r2.Raw) != string(r3.Raw) {
		t.Errorf("responses must be identical: %s | %s | %s", r1.Raw, r2.Raw, r3.Raw)
	}
	if r := c.Post("/api/v1/auth/otp/request", map[string]any{"email": "not-an-email"}); r.Code != 422 {
		t.Errorf("invalid e-mail: %d", r.Code)
	}
}

func TestOTPRateLimitByEmail(t *testing.T) {
	env := servertest.New(t)
	c := env.Client()
	for i := 0; i < 5; i++ {
		if r := c.Post("/api/v1/auth/otp/request", map[string]any{"email": "eve@example.com"}); r.Code != 202 {
			t.Fatalf("request %d: %d", i, r.Code)
		}
	}
	r := c.Post("/api/v1/auth/otp/request", map[string]any{"email": "eve@example.com"})
	if r.Code != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("6th request must be 429: %d %s", r.Code, r.Raw)
	}
}

func TestOTPRedisDownFailsClosed(t *testing.T) {
	env := servertest.New(t)
	_ = env.Server.Redis.Close()
	r := env.Client().Post("/api/v1/auth/otp/request", map[string]any{"email": "x@example.com"})
	if r.Code != 503 || r.ErrCode() != "unavailable" {
		t.Fatalf("want 503: %d %s", r.Code, r.Raw)
	}
}

func TestRegistrationWithSlugAndNickname(t *testing.T) {
	env := servertest.New(t)
	a := env.Client()
	r := env.SignIn(a, "first@example.com", map[string]any{"nickname": "Business Expert", "slug": "business-expert"})
	u := r.Data()["user"].(map[string]any)
	if r.Code != 200 || u["slug"] != "business-expert" || u["nickname"] != "Business Expert" {
		t.Fatalf("%d %s", r.Code, r.Raw)
	}
	// taken slug: 409 and the code is NOT consumed
	b := env.Client()
	b.Post("/api/v1/auth/otp/request", map[string]any{"email": "second@example.com"})
	code := ""
	for _, w := range strings.Fields(env.Mailer.Messages()[len(env.Mailer.Messages())-1].TextBody) {
		if len(w) == 6 && strings.Trim(w, "0123456789") == "" {
			code = w
		}
	}
	body := map[string]any{"email": "second@example.com", "code": code, "accept_terms": true, "slug": "Business-Expert"}
	if r := b.Post("/api/v1/auth/otp/verify", body); r.Code != 409 {
		t.Fatalf("taken slug: %d %s", r.Code, r.Raw)
	}
	body["slug"] = "admin"
	if r := b.Post("/api/v1/auth/otp/verify", body); r.Code != 422 {
		t.Fatalf("reserved slug: %d %s", r.Code, r.Raw)
	}
	body["slug"] = "free-name"
	if r := b.Post("/api/v1/auth/otp/verify", body); r.Code != 200 {
		t.Fatalf("code must still be valid after slug errors: %d %s", r.Code, r.Raw)
	}
}

func TestProfileAndSlugEndpoints(t *testing.T) {
	env := servertest.New(t)
	c := env.SignInSlug("fay@example.com", "fay", "Fay")
	// PATCH validation
	if r := c.Patch("/api/v1/me", map[string]any{"nickname": "   ", "locale": "xx", "bio": strings.Repeat("я", 501)}); r.Code != 422 {
		t.Fatalf("patch invalid: %d %s", r.Code, r.Raw)
	}
	r := c.Patch("/api/v1/me", map[string]any{"nickname": "Fay B", "bio": "Hi", "locale": "bg"})
	if r.Code != 200 || r.Data()["nickname"] != "Fay B" || r.Data()["locale"] != "bg" {
		t.Fatalf("patch: %d %s", r.Code, r.Raw)
	}
	if r := c.Patch("/api/v1/me", map[string]any{"email": "x@y.z"}); r.Code != 400 {
		t.Errorf("unknown fields rejected: %d", r.Code)
	}
	// slug check + change
	if r := c.Get("/api/v1/slugs/check?type=user&slug=admin"); r.Data()["available"] != false {
		t.Error("reserved is unavailable")
	}
	if r := c.Get("/api/v1/slugs/check?type=user&slug=fresh-name"); r.Data()["available"] != true {
		t.Error("fresh is available")
	}
	if r := c.Put("/api/v1/me/slug", map[string]any{"slug": "fay-b"}); r.Code != 200 || r.Data()["slug"] != "fay-b" {
		t.Fatalf("slug change: %d %s", r.Code, r.Raw)
	}
	if r := c.Put("/api/v1/me/slug", map[string]any{"slug": "fay-c"}); r.Code != 409 {
		t.Fatalf("second change too soon: %d %s", r.Code, r.Raw)
	}
	// public profile: old slug gives canonical_slug; no private fields
	anon := env.Client()
	pr := anon.Get("/api/v1/users/fay")
	if pr.Code != 200 || pr.Data()["canonical_slug"] != "fay-b" || pr.Data()["nickname"] != "Fay B" || pr.Data()["activity_visible"] != true {
		t.Fatalf("public profile: %d %s", pr.Code, pr.Raw)
	}
	for _, k := range []string{"email", "status", "locale", "roles"} {
		if _, ok := pr.Data()[k]; ok {
			t.Errorf("public profile must not expose %s", k)
		}
	}
	// hide activity
	c.Patch("/api/v1/me", map[string]any{"profile_public": false})
	pr = anon.Get("/api/v1/users/fay-b")
	if pr.Data()["activity_visible"] != false || pr.Data()["bio"] != "" {
		t.Fatalf("hidden activity: %s", pr.Raw)
	}
	if anon.Get("/api/v1/users/nobody-here").Code != 404 {
		t.Error("unknown slug 404")
	}
}

func TestBlockedAndPendingUsers(t *testing.T) {
	env := servertest.New(t)
	ctx := context.Background()
	c := env.LoginAs("gus@example.com")
	id := c.UserID()
	// pending_deletion: still authenticated, but not active
	env.Server.Pool.Exec(ctx, `UPDATE users SET status='pending_deletion' WHERE id=$1`, id)
	if c.Get("/api/v1/me").Code != 200 {
		t.Error("pending user can read /me")
	}
	if r := c.Patch("/api/v1/me", map[string]any{"bio": "x"}); r.Code != 403 {
		t.Errorf("pending user cannot modify: %d", r.Code)
	}
	// blocked: old session stops working at once and new sign-in is refused
	env.Server.Pool.Exec(ctx, `UPDATE users SET status='blocked' WHERE id=$1`, id)
	if c.Get("/api/v1/me").Code != 401 {
		t.Error("blocked user's session must be dead")
	}
	c2 := env.Client()
	r := env.SignIn(c2, "gus@example.com", nil)
	if r.Code != 403 {
		t.Errorf("blocked sign-in: %d %s", r.Code, r.Raw)
	}
	if anon := env.Client().Get("/api/v1/users/gus"); anon.Code != 404 {
		t.Errorf("blocked profile hidden: %d", anon.Code)
	}
}

func TestCSRFAndDevLogin(t *testing.T) {
	env := servertest.New(t)
	c := env.Client()
	// POST without the custom header and Origin is refused
	req := c.Do("POST", "/api/v1/auth/otp/request", `{"email":"a@b.co"}`)
	if req.Code != 202 {
		t.Fatalf("sanity: %d", req.Code)
	}
	if r := c.Do("POST", "/api/v1/dev/login", map[string]any{"email": "dev@example.com"}); r.Code != 200 {
		t.Fatalf("dev login in dev: %d %s", r.Code, r.Raw)
	}
	prod := servertest.NewWith(t, func(cfg *config.Config) {
		cfg.AppEnv = "prod"
		cfg.ProxySharedSecret = strings.Repeat("p", 40)
		cfg.TrustedProxyCIDRs = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
	})
	pc := prod.Client()
	pc.Headers = map[string]string{"X-Proxy-Secret": strings.Repeat("p", 40)}
	if r := pc.Do("POST", "/api/v1/dev/login", map[string]any{"email": "dev@example.com"}); r.Code != 404 {
		t.Fatalf("dev login must not exist in prod: %d", r.Code)
	}
	if r := pc.Get("/api/v1/auth/oauth/google/start"); r.Code != 404 {
		t.Fatalf("provider without credentials: %d", r.Code)
	}
	if r := prod.Client().Get("/api/v1/ideas"); r.Code != 403 {
		t.Fatalf("direct access without the Worker secret must be refused: %d", r.Code)
	}
}
