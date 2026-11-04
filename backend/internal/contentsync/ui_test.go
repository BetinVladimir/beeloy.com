package contentsync_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"beeloy.com/backend/internal/contentsync"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/s3store"
	"beeloy.com/backend/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

// publishRelease writes i18n/releases/<v>/<locale>.json for all 26 locales and the manifest (last).
// mutate may alter the per-locale messages or the manifest before it is written.
func publishRelease(t *testing.T, st *s3store.MemStore, version string, mutate func(locale string, msgs map[string]string)) {
	t.Helper()
	m := contentsync.Manifest{SchemaVersion: 1, Version: version, PublishedAt: time.Now().UTC(), ReferenceLocale: "en"}
	for _, code := range locales.Codes() {
		msgs := map[string]string{"app.name": "Beeloy", "greet.user": "Hello {name}", "items.one": "{count} item", "items.other": "{count} items"}
		if code != "en" {
			msgs["greet.user"] = code + " {name}"
			msgs["items.one"] = code + " {count}"
			msgs["items.other"] = code + " {count}!"
		}
		if mutate != nil {
			mutate(code, msgs)
		}
		raw, _ := json.Marshal(msgs)
		sum := sha256.Sum256(raw)
		rel := fmt.Sprintf("releases/%s/%s.json", version, code)
		st.Put(context.Background(), "i18n/"+rel, raw, "application/json")
		m.Locales = append(m.Locales, contentsync.ManifestItem{Code: code, File: rel, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw))})
	}
	raw, _ := json.Marshal(m)
	st.Put(context.Background(), "i18n/manifest.json", raw, "application/json")
}

func newSyncer(t *testing.T) (*contentsync.Syncer, *s3store.MemStore, *contentsync.Store) {
	pool := testutil.PG(t)
	st := s3store.NewMem()
	store := &contentsync.Store{Pool: pool}
	return &contentsync.Syncer{Src: st, Store: store, RetryDelay: time.Millisecond}, st, store
}

func active(t *testing.T, s *contentsync.Store) string {
	v, _, err := s.CurrentActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSyncActivatesAndIsIdempotent(t *testing.T) {
	sy, st, store := newSyncer(t)
	ctx := context.Background()
	publishRelease(t, st, "2026-11-04.1", nil)
	manifestV1, _ := st.Get(ctx, "i18n/manifest.json", 1<<20)
	res, v, err := sy.RunOnce(ctx)
	if err != nil || res != contentsync.ResultOK || v != "2026-11-04.1" || active(t, store) != "2026-11-04.1" {
		t.Fatalf("first run: %v %v %v", res, v, err)
	}
	var n int
	store.Pool.QueryRow(ctx, `SELECT count(*) FROM i18n_bundles WHERE version='2026-11-04.1'`).Scan(&n)
	if n != 26 {
		t.Fatalf("bundles: %d", n)
	}
	if res, _, _ := sy.RunOnce(ctx); res != contentsync.ResultNoop {
		t.Fatalf("repeat run must be noop, got %s", res)
	}
	// a new version replaces the old one atomically
	publishRelease(t, st, "2026-11-05.1", nil)
	if res, _, err := sy.RunOnce(ctx); err != nil || res != contentsync.ResultOK || active(t, store) != "2026-11-05.1" {
		t.Fatalf("second release: %v %v", res, err)
	}
	var old string
	store.Pool.QueryRow(ctx, `SELECT status FROM i18n_releases WHERE version='2026-11-04.1'`).Scan(&old)
	if old != "superseded" {
		t.Errorf("old release status: %s", old)
	}
	// a rollback is refused while the S3 manifest still points to the newer release (polling would undo it)
	if err := sy.Rollback(ctx, "2026-11-04.1"); !errors.Is(err, contentsync.ErrRollbackConflict) || active(t, store) != "2026-11-05.1" {
		t.Fatalf("rollback without a restored manifest must conflict: %v", err)
	}
	// the operator puts the old manifest back; now the rollback is accepted and polling keeps it
	if err := st.Put(ctx, "i18n/manifest.json", manifestV1, "application/json"); err != nil {
		t.Fatal(err)
	}
	if err := sy.Rollback(ctx, "2026-11-04.1"); err != nil || active(t, store) != "2026-11-04.1" {
		t.Fatalf("rollback: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := sy.RunOnce(ctx); err != nil || active(t, store) != "2026-11-04.1" {
			t.Fatalf("polling changed the rolled-back release: %v %s", err, active(t, store))
		}
	}
	if err := sy.Rollback(ctx, "nope"); err == nil {
		t.Error("rollback to unknown version must fail")
	}
}

func TestBrokenReleaseNeverChangesActiveVersion(t *testing.T) {
	sy, st, store := newSyncer(t)
	ctx := context.Background()
	publishRelease(t, st, "v1", nil)
	if res, _, err := sy.RunOnce(ctx); err != nil || res != contentsync.ResultOK {
		t.Fatal(err)
	}
	cases := map[string]func(locale string, msgs map[string]string){
		"missing key": func(l string, m map[string]string) {
			if l == "de" {
				delete(m, "app.name")
			}
		},
		"extra key": func(l string, m map[string]string) {
			if l == "fr" {
				m["zzz.extra"] = "x"
			}
		},
		"placeholder differs": func(l string, m map[string]string) {
			if l == "bg" {
				m["greet.user"] = "Здравей {nam}"
			}
		},
		"bad key": func(l string, m map[string]string) {
			if l == "ru" {
				m["Bad Key"] = "x"
			}
		},
	}
	i := 0
	for name, mut := range cases {
		i++
		v := fmt.Sprintf("bad%d", i)
		publishRelease(t, st, v, mut)
		res, _, err := sy.RunOnce(ctx)
		if err == nil || res != contentsync.ResultFailed {
			t.Fatalf("%s: want failure, got %v %v", name, res, err)
		}
		if active(t, store) != "v1" {
			t.Fatalf("%s: active version changed to %s", name, active(t, store))
		}
		var status string
		store.Pool.QueryRow(ctx, `SELECT status FROM i18n_releases WHERE version=$1`, v).Scan(&status)
		if status != "failed" {
			t.Errorf("%s: release status %q", name, status)
		}
	}
	// corrupted checksum of one file
	publishRelease(t, st, "v2", nil)
	st.Put(ctx, "i18n/releases/v2/pl.json", []byte(`{"app.name":"tampered"}`), "application/json")
	if res, _, err := sy.RunOnce(ctx); err == nil || res != contentsync.ResultFailed || active(t, store) != "v1" {
		t.Fatalf("tampered file: %v %v active=%s", res, err, active(t, store))
	}
	// a manifest with a missing language
	publishRelease(t, st, "v3", nil)
	var m contentsync.Manifest
	raw, _ := st.Get(ctx, "i18n/manifest.json", 1<<20)
	json.Unmarshal(raw, &m)
	m.Locales = m.Locales[:25]
	raw, _ = json.Marshal(m)
	st.Put(ctx, "i18n/manifest.json", raw, "application/json")
	if res, _, err := sy.RunOnce(ctx); err == nil || res != contentsync.ResultFailed || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("incomplete manifest: %v %v", res, err)
	}
	// problems are reported together in sync_runs
	var e string
	store.Pool.QueryRow(ctx, `SELECT error FROM sync_runs WHERE result='failed' ORDER BY started_at DESC LIMIT 1`).Scan(&e)
	if e == "" {
		t.Error("failed run must keep its error")
	}
	// a failed version can be retried after the files are fixed
	publishRelease(t, st, "bad1", nil)
	if res, _, err := sy.RunOnce(ctx); err != nil || res != contentsync.ResultOK || active(t, store) != "bad1" {
		t.Fatalf("retry of failed version: %v %v", res, err)
	}
}

func TestConcurrentRunsOnlyOneWorks(t *testing.T) {
	sy, st, store := newSyncer(t)
	publishRelease(t, st, "c1", nil)
	var wg sync.WaitGroup
	results := make(chan contentsync.Result, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _, _ := sy.RunOnce(context.Background())
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for r := range results {
		if r == contentsync.ResultOK {
			ok++
		}
	}
	if ok != 1 || active(t, store) != "c1" {
		t.Fatalf("exactly one run must activate, ok=%d", ok)
	}
}

func TestPruneKeepsActive(t *testing.T) {
	sy, st, store := newSyncer(t)
	ctx := context.Background()
	for i := 1; i <= 8; i++ {
		publishRelease(t, st, fmt.Sprintf("r%d", i), nil)
		if res, _, err := sy.RunOnce(ctx); err != nil || res != contentsync.ResultOK {
			t.Fatalf("r%d: %v %v", i, res, err)
		}
	}
	var n int
	store.Pool.QueryRow(ctx, `SELECT count(*) FROM i18n_releases`).Scan(&n)
	if n != 5 || active(t, store) != "r8" {
		t.Fatalf("releases kept: %d (active %s)", n, active(t, store))
	}
	// versions are never reused
	publishRelease(t, st, "r8", func(l string, m map[string]string) { m["app.name"] = "changed" })
	if res, _, _ := sy.RunOnce(ctx); res != contentsync.ResultNoop {
		t.Fatalf("same version again: %s", res)
	}
}

func TestValidateUnits(t *testing.T) {
	good := contentsync.Manifest{SchemaVersion: 1, Version: "v1", ReferenceLocale: "en"}
	sum := strings.Repeat("a", 64)
	for _, c := range []string{"en", "bg"} {
		good.Locales = append(good.Locales, contentsync.ManifestItem{Code: c, File: "releases/v1/" + c + ".json", SHA256: sum, Size: 10})
	}
	req := []string{"en", "bg"}
	if err := contentsync.ValidateManifest(good, req); err != nil {
		t.Fatal(err)
	}
	bad := func(mut func(m *contentsync.Manifest)) error {
		m := good
		m.Locales = append([]contentsync.ManifestItem(nil), good.Locales...)
		mut(&m)
		return contentsync.ValidateManifest(m, req)
	}
	for name, mut := range map[string]func(m *contentsync.Manifest){
		"dup locale": func(m *contentsync.Manifest) { m.Locales[1].Code = "en" },
		"extra locale": func(m *contentsync.Manifest) {
			m.Locales = append(m.Locales, contentsync.ManifestItem{Code: "tr", File: "x", SHA256: sum, Size: 1})
		},
		"dotdot":      func(m *contentsync.Manifest) { m.Locales[0].File = "../secret.json" },
		"abs path":    func(m *contentsync.Manifest) { m.Locales[0].File = "/etc/passwd" },
		"huge":        func(m *contentsync.Manifest) { m.Locales[0].Size = 2 << 20 },
		"bad version": func(m *contentsync.Manifest) { m.Version = "v 1/../" },
		"bad schema":  func(m *contentsync.Manifest) { m.SchemaVersion = 2 },
		"bad ref":     func(m *contentsync.Manifest) { m.ReferenceLocale = "xx" },
	} {
		if bad(mut) == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	item := contentsync.ManifestItem{Code: "en", Size: 2}
	if _, err := contentsync.ValidateBundle(item, []byte("[]")); err == nil {
		t.Error("array and wrong sha must fail")
	}
	raw := []byte(`["a"]`)
	s := sha256.Sum256(raw)
	if _, err := contentsync.ValidateBundle(contentsync.ManifestItem{Code: "en", Size: int64(len(raw)), SHA256: hex.EncodeToString(s[:])}, raw); err == nil {
		t.Error("array instead of object must fail")
	}
	raw = []byte(`{"a.b":1}`)
	s = sha256.Sum256(raw)
	if _, err := contentsync.ValidateBundle(contentsync.ManifestItem{Code: "en", Size: int64(len(raw)), SHA256: hex.EncodeToString(s[:])}, raw); err == nil {
		t.Error("non-string value must fail")
	}
}

func TestSeedTemplateActivatesOnEmptyBucketAndNeverOverwrites(t *testing.T) {
	syncer, st, store := newSyncer(t)
	ctx := context.Background()
	ref := []byte(`{"app.name":"Beeloy","greeting":"Hello, {name}"}`)
	if _, _, err := syncer.RunOnce(ctx); err == nil {
		t.Fatal("an empty bucket has no manifest")
	}
	ok, err := contentsync.SeedTemplate(ctx, st, ref, locales.Source(), locales.Codes())
	if err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	if res, v, err := syncer.RunOnce(ctx); err != nil || res != contentsync.ResultOK || v != contentsync.SeedVersion {
		t.Fatalf("sync: %v %v %v", res, v, err)
	}
	if active(t, store) != contentsync.SeedVersion {
		t.Error("template release must become active")
	}
	if ok, err := contentsync.SeedTemplate(ctx, st, ref, locales.Source(), locales.Codes()); err != nil || ok {
		t.Errorf("second seed must be a no-op: %v %v", ok, err)
	}
}
