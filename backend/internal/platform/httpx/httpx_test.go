package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"beeloy.com/backend/internal/apperr"
)

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("bad json %q: %v", rec.Body.String(), err)
	}
	return m
}

func TestWriteErrorFormats(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{apperr.ErrNotFound, 404, "not_found"},
		{apperr.Field("title", "too_short"), 422, "validation_failed"},
		{apperr.RateLimited("slow", 30), 429, "rate_limited"},
		{errors.New("pg exploded: secret"), 500, "internal"},
		{apperr.ErrPayloadTooLarge, 413, "payload_too_large"},
		{apperr.ErrInvalidCode, 400, "invalid_code"},
		{apperr.ErrUnavailable, 503, "unavailable"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		WriteError(rec, httptest.NewRequest("GET", "/x", nil), c.err)
		m := decode(t, rec)
		e := m["error"].(map[string]any)
		if rec.Code != c.status || e["code"] != c.code {
			t.Errorf("%v => %d %v", c.err, rec.Code, e)
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Error("cause must never leak")
		}
	}
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest("GET", "/x", nil), apperr.RateLimited("slow", 30))
	if rec.Header().Get("Retry-After") != "30" {
		t.Error("Retry-After header")
	}
	rec = httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest("GET", "/x", nil), apperr.Field("slug", "reserved"))
	if f := decode(t, rec)["error"].(map[string]any)["fields"].(map[string]any); f["slug"] != "reserved" {
		t.Error("fields")
	}
}

func TestDecodeJSON(t *testing.T) {
	type req struct {
		A string `json:"a"`
	}
	ok := func(body string) error {
		var v req
		return DecodeJSON(httptest.NewRequest("POST", "/", strings.NewReader(body)), &v)
	}
	if err := ok(`{"a":"x"}`); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"a":"x","b":1}`, `{"a":"x"} {"a":"y"}`, `[]`, ``, `{`} {
		if ok(bad) == nil {
			t.Errorf("%q must fail", bad)
		}
	}
}

func TestListAndEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	JSONList(rec, nil, "")
	m := decode(t, rec)
	if _, ok := m["data"].([]any); !ok || m["next_cursor"] != nil {
		t.Fatalf("list: %v", m)
	}
	rec = httptest.NewRecorder()
	JSONWith(rec, 200, map[string]any{"x": 1}, map[string]any{"canonical_slug": "a"})
	if decode(t, rec)["canonical_slug"] != "a" {
		t.Fatal("extra")
	}
}

func TestCSRF(t *testing.T) {
	h := CSRF([]string{"https://beeloy.com"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	do := func(method, origin, xrw string) int {
		r := httptest.NewRequest(method, "/api/v1/x", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if xrw != "" {
			r.Header.Set("X-Requested-With", xrw)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if do("GET", "", "") != 204 {
		t.Error("GET passes")
	}
	if do("POST", "", "") != 403 || do("POST", "https://beeloy.com", "") != 403 || do("POST", "https://evil.com", "beeloy") != 403 {
		t.Error("POST must be checked")
	}
	if do("POST", "https://beeloy.com", "beeloy") != 204 || do("DELETE", "https://beeloy.com", "beeloy") != 204 {
		t.Error("valid POST/DELETE")
	}
}

func TestMaxBody(t *testing.T) {
	h := MaxBody(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		if err := DecodeJSON(r, &v); err != nil {
			WriteError(w, r, err)
			return
		}
		w.WriteHeader(200)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"a":"`+strings.Repeat("x", 100)+`"}`)))
	if rec.Code != 413 {
		t.Fatalf("want 413 got %d", rec.Code)
	}
}

func TestRealIP(t *testing.T) {
	cfg := RealIPConfig{TrustedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, SharedSecret: "s3cret-s3cret-s3cret-s3cret-xx"}
	var got netip.Addr
	h := RealIP(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = ClientIP(r) }))
	run := func(remote string, hdr map[string]string) netip.Addr {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		return got
	}
	if run("10.1.1.1:5", map[string]string{"X-Proxy-Secret": cfg.SharedSecret, "X-Real-Client-IP": "203.0.113.9"}).String() != "203.0.113.9" {
		t.Error("shared secret path")
	}
	if run("10.1.1.1:5", map[string]string{"X-Proxy-Secret": "wrong", "X-Real-Client-IP": "203.0.113.9", "CF-Connecting-IP": "198.51.100.7"}).String() != "198.51.100.7" {
		t.Error("wrong secret falls back to CF-Connecting-IP")
	}
	if run("192.0.2.5:5", map[string]string{"X-Proxy-Secret": cfg.SharedSecret, "X-Real-Client-IP": "203.0.113.9", "CF-Connecting-IP": "198.51.100.7"}).String() != "192.0.2.5" {
		t.Error("untrusted peer must be ignored")
	}
}

func TestRecoverer(t *testing.T) {
	h := RequestID(Recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("boom") })))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 || rec.Header().Get("X-Request-Id") == "" || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("recoverer: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRequireTrustedIngress(t *testing.T) {
	cfg := RealIPConfig{TrustedCIDRs: []netip.Prefix{netip.MustParsePrefix("172.20.0.0/24")}, SharedSecret: "s3cret-s3cret-s3cret-s3cret-s3cret"}
	h := RealIP(cfg)(RequireTrustedIngress(cfg, true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })))
	do := func(method, path, peer, secret string) int {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = peer
		if secret != "" {
			req.Header.Set("X-Proxy-Secret", secret)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	good, caddy, other := cfg.SharedSecret, "172.20.0.5:443", "203.0.113.9:443"
	cases := []struct {
		name                       string
		method, path, peer, secret string
		want                       int
	}{
		{"caddy with secret", "GET", "/api/v1/ideas", caddy, good, 204},
		{"caddy without secret", "GET", "/api/v1/ideas", caddy, "", 403},
		{"wrong secret", "GET", "/api/v1/ideas", caddy, "nope", 403},
		{"secret from an untrusted peer", "GET", "/api/v1/ideas", other, good, 403},
		{"public dictionary without secret", "GET", "/api/v1/i18n/bg", caddy, "", 204},
		{"public dictionary from an untrusted peer", "GET", "/api/v1/i18n/bg", other, "", 403},
		{"categories without secret", "GET", "/api/v1/categories", caddy, "", 204},
		{"exception is GET-only", "POST", "/api/v1/i18n/bg", caddy, "", 403},
		{"no sub-paths in the exception", "GET", "/api/v1/i18n/bg/x", caddy, "", 403},
		{"OAuth callback needs the secret", "GET", "/api/v1/auth/oauth/google/callback", caddy, "", 403},
	}
	for _, c := range cases {
		if got := do(c.method, c.path, c.peer, c.secret); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}
