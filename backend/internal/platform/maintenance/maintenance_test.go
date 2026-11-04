package maintenance_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"beeloy.com/backend/internal/platform/maintenance"
	"beeloy.com/backend/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

func TestWritesAreFrozenButReadsAreNot(t *testing.T) {
	rdb := testutil.Redis(t)
	h := maintenance.Middleware(rdb)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	do := func(method string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/x", nil))
		return rec.Code
	}
	if do("POST") != 204 {
		t.Fatal("writes pass without the flag")
	}
	rdb.Set(t.Context(), maintenance.Key, "1", time.Minute)
	if do("POST") != 503 || do("DELETE") != 503 {
		t.Error("writes must be refused during the freeze")
	}
	if do("GET") != 204 {
		t.Error("reads stay available")
	}
	if !maintenance.On(t.Context(), rdb) {
		t.Error("On")
	}
	rdb.Del(t.Context(), maintenance.Key)
	if do("POST") != 204 {
		t.Error("writes resume after the flag is removed")
	}
}
