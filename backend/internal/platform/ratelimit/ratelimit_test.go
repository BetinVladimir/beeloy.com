package ratelimit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/ratelimit"
	"beeloy.com/backend/internal/testutil"
	"errors"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

func TestAllowWindow(t *testing.T) {
	l := ratelimit.New(testutil.Redis(t))
	rule := ratelimit.Rule{Name: "t", Limit: 3, Window: 1500 * time.Millisecond}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if ok, _, err := l.Allow(ctx, rule, "a"); err != nil || !ok {
			t.Fatalf("event %d must pass: %v", i, err)
		}
	}
	ok, retry, err := l.Allow(ctx, rule, "a")
	if err != nil || ok || retry <= 0 {
		t.Fatalf("4th must be rejected with Retry-After: %v %v %v", ok, retry, err)
	}
	if ok, _, _ := l.Allow(ctx, rule, "other"); !ok {
		t.Fatal("other id has its own counter")
	}
	time.Sleep(1700 * time.Millisecond)
	if ok, _, _ := l.Allow(ctx, rule, "a"); !ok {
		t.Fatal("window must expire")
	}
}

func TestCheckAndMiddleware(t *testing.T) {
	rdb := testutil.Redis(t)
	l := ratelimit.New(rdb)
	rule := ratelimit.Rule{Name: "m", Limit: 1, Window: time.Minute}
	h := l.Middleware(rule, func(*http.Request) string { return "k" }, true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("want 429 with Retry-After: %d %v", rec.Code, rec.Header())
	}
	// Redis down
	_ = rdb.Close()
	err := l.Check(context.Background(), rule, "z", true)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.HTTP != 503 {
		t.Fatalf("fail-closed must give 503: %v", err)
	}
	if err := l.Check(context.Background(), rule, "z", false); err != nil {
		t.Fatalf("fail-open must pass: %v", err)
	}
}
