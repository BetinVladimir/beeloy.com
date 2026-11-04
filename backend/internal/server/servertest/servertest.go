// Package servertest builds a complete API server on disposable PostgreSQL/Redis for HTTP integration tests.
package servertest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/config"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/platform/ids"
	pmail "beeloy.com/backend/internal/platform/mail"
	"beeloy.com/backend/internal/platform/s3store"
	"beeloy.com/backend/internal/server"
	"beeloy.com/backend/internal/testutil"
)

const Origin = "https://beeloy.com"

type Env struct {
	T      testing.TB
	Server *server.Server
	Mailer *pmail.MemMailer
	Cfg    *config.Config
	Store  *s3store.MemStore
}

// New starts a server with default dev configuration.
func New(t testing.TB) *Env { return NewWith(t, nil) }

// NewWith lets a test tweak the configuration before the server is built.
func NewWith(t testing.TB, tweak func(*config.Config)) *Env {
	t.Helper()
	cfg, err := config.LoadFrom(func(k string) string {
		return map[string]string{
			"APP_ENV": "dev", "PUBLIC_BASE_URL": Origin, "ALLOWED_ORIGINS": Origin, "DATABASE_URL": "x", "REDIS_URL": "x",
			"OTP_HMAC_SECRET": strings.Repeat("k", 40), "TERMS_VERSION": "t1", "PRIVACY_VERSION": "p1",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	if tweak != nil {
		tweak(cfg)
	}
	mailer := &pmail.MemMailer{}
	store := s3store.NewMem()
	srv := server.New(server.Deps{Cfg: cfg, Pool: testutil.PG(t), Redis: testutil.Redis(t), Mailer: mailer, Store: store})
	return &Env{T: t, Server: srv, Mailer: mailer, Cfg: cfg, Store: store}
}

// Client is an HTTP client with its own cookie jar (one browser).
type Client struct {
	env     *Env
	Cookies map[string]string
	IP      string
	Headers map[string]string // extra request headers for the next calls
}

func (e *Env) Client() *Client {
	return &Client{env: e, Cookies: map[string]string{}, IP: "198.51.100.10:1234"}
}

type Resp struct {
	Code   int
	Header http.Header
	Raw    []byte
	JSON   map[string]any
}

// Do sends a request; body may be nil, a string (raw JSON) or any value (marshalled).
func (c *Client) Do(method, path string, body any) *Resp {
	c.env.T.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = c.IP
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", Origin)
		req.Header.Set("X-Requested-With", "beeloy")
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range c.Cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	rec := httptest.NewRecorder()
	c.env.Server.Router.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.MaxAge < 0 || ck.Value == "" {
			delete(c.Cookies, ck.Name)
		} else {
			c.Cookies[ck.Name] = ck.Value
		}
	}
	r := &Resp{Code: rec.Code, Header: rec.Header(), Raw: rec.Body.Bytes()}
	_ = json.Unmarshal(r.Raw, &r.JSON)
	return r
}

func (c *Client) Get(path string) *Resp          { c.env.T.Helper(); return c.Do("GET", path, nil) }
func (c *Client) Post(path string, b any) *Resp  { c.env.T.Helper(); return c.Do("POST", path, b) }
func (c *Client) Put(path string, b any) *Resp   { c.env.T.Helper(); return c.Do("PUT", path, b) }
func (c *Client) Patch(path string, b any) *Resp { c.env.T.Helper(); return c.Do("PATCH", path, b) }
func (c *Client) Delete(path string) *Resp       { c.env.T.Helper(); return c.Do("DELETE", path, nil) }
func (c *Client) WithIP(ip string) *Client       { c.IP = ip + ":1234"; return c }

// Data returns r.JSON["data"] as an object (nil if absent).
func (r *Resp) Data() map[string]any { m, _ := r.JSON["data"].(map[string]any); return m }

// List returns r.JSON["data"] as an array.
func (r *Resp) List() []any { l, _ := r.JSON["data"].([]any); return l }

// ErrCode returns error.code ("" if none).
func (r *Resp) ErrCode() string {
	e, _ := r.JSON["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

var codeRe = regexp.MustCompile(`\b(\d{6})\b`)

// LoginAs signs a (new or existing) user in through the real OTP flow and returns the client.
func (e *Env) LoginAs(email string) *Client {
	e.T.Helper()
	c := e.Client()
	// tests read better with a public name derived from the address; real sign-ups stay neutral (see OTP tests)
	local := email[:strings.IndexByte(email, '@')]
	extra := map[string]any{"nickname": local}
	if slug := identity.Slugify(local); identity.Validate(identity.EntityUser, slug) == nil {
		extra["slug"] = slug
	}
	e.SignIn(c, email, extra)
	return c
}

// SignIn performs request+verify; extra holds optional fields (nickname, slug, accept_terms=false to test rejection).
func (e *Env) SignIn(c *Client, email string, extra map[string]any) *Resp {
	e.T.Helper()
	// tests may sign in repeatedly: lift the 60 s resend cooldown (the cooldown itself is covered by its own test)
	if norm, err := identity.NormalizeEmail(email); err == nil {
		e.Server.Redis.Del(context.Background(), "otp:cool:"+norm)
	}
	before := len(e.Mailer.Messages())
	if r := c.Post("/api/v1/auth/otp/request", map[string]any{"email": email, "locale": "en"}); r.Code != 202 {
		e.T.Fatalf("otp request: %d %s", r.Code, r.Raw)
	}
	msgs := e.Mailer.Messages()
	if len(msgs) != before+1 {
		e.T.Fatalf("expected one mail, got %d", len(msgs)-before)
	}
	m := codeRe.FindString(msgs[len(msgs)-1].TextBody)
	body := map[string]any{"email": email, "code": m, "locale": "en", "accept_terms": true}
	for k, v := range extra {
		body[k] = v
	}
	return c.Post("/api/v1/auth/otp/verify", body)
}

// UserID returns the id of the user the client is signed in as.
func (c *Client) UserID() string {
	c.env.T.Helper()
	r := c.Get("/api/v1/me")
	if r.Code != 200 {
		c.env.T.Fatalf("me: %d %s", r.Code, r.Raw)
	}
	return r.Data()["id"].(string)
}

// Grant gives the user a role directly in the database (scopeCategory nil = global).
func (e *Env) Grant(userID string, role acl.Role, scopeCategory *string) {
	e.T.Helper()
	b := acl.Binding{ID: ids.New(), Role: role, ScopeType: acl.ScopeGlobal}
	if scopeCategory != nil {
		id := uuid.MustParse(*scopeCategory)
		b.ScopeType, b.ScopeID = acl.ScopeCategory, &id
	}
	uid := uuid.MustParse(userID)
	if err := (acl.Repo{}).InsertBinding(context.Background(), e.Server.Pool, b, uid, uid); err != nil {
		e.T.Fatal(err)
	}
}

// Flush publishes pending outbox events to Redis (the relay is not running in tests).
func (e *Env) Flush() {
	e.T.Helper()
	if _, err := e.Server.Relay.FlushOnce(context.Background()); err != nil {
		e.T.Fatal(err)
	}
}

// Seeded category ids (migration 0012).
const (
	CatNews      = "0198f000-0000-7000-8000-000000000001"
	CatCommunity = "0198f000-0000-7000-8000-000000000002"
	CatProduct   = "0198f000-0000-7000-8000-000000000003"
)

// SignInSlug registers a user with a chosen slug and nickname.
func (e *Env) SignInSlug(email, slug, nickname string) *Client {
	e.T.Helper()
	c := e.Client()
	r := e.SignIn(c, email, map[string]any{"slug": slug, "nickname": nickname})
	if r.Code != 200 {
		e.T.Fatalf("sign in %s: %d %s", email, r.Code, r.Raw)
	}
	return c
}

// StartBackground runs the outbox relay and the realtime consumer until the test ends.
func (e *Env) StartBackground() {
	e.T.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.Server.StartBackground(ctx)
	e.T.Cleanup(func() { cancel(); e.Server.Wait() })
}

// HTTPServer exposes the router on a real listener (needed for streaming responses such as SSE).
func (e *Env) HTTPServer() *httptest.Server {
	e.T.Helper()
	srv := httptest.NewServer(e.Server.Router)
	e.T.Cleanup(srv.Close)
	return srv
}

// Cookie returns the session cookie value of the client.
func (c *Client) Cookie() string { return c.Cookies["bl_session"] }
