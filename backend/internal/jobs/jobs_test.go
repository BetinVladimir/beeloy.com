package jobs_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"beeloy.com/backend/internal/platform/metrics"
	"beeloy.com/backend/internal/platform/outbox"
	"beeloy.com/backend/internal/server/servertest"
	"beeloy.com/backend/internal/testutil"
	"io"
	"net/http/httptest"
	"strings"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

func TestTaskLockPreventsParallelRuns(t *testing.T) {
	env := servertest.New(t)
	ctx := context.Background()
	// hold the purge lock on another connection: the task must be skipped
	conn, _ := env.Server.Pool.Acquire(ctx)
	defer conn.Release()
	var ok bool
	conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(7001)`).Scan(&ok)
	if !ok {
		t.Fatal("lock")
	}
	if ran, err := env.Server.Jobs.RunTask(ctx, "PurgeDeletedAccounts"); err != nil || ran {
		t.Fatalf("locked task must be skipped: ran=%v err=%v", ran, err)
	}
	conn.Exec(ctx, `SELECT pg_advisory_unlock(7001)`)
	if ran, _ := env.Server.Jobs.RunTask(ctx, "PurgeDeletedAccounts"); !ran {
		t.Fatal("after unlock the task runs")
	}
	if ran, _ := env.Server.Jobs.RunTask(ctx, "NoSuchTask"); ran {
		t.Fatal("unknown task")
	}
}

func TestMaintenanceTasks(t *testing.T) {
	env := servertest.New(t)
	ctx := context.Background()
	p := env.Server.Pool
	u := testutil.InsertUser(t, p, "x")
	p.Exec(ctx, `INSERT INTO sessions (id, user_id, token_hash, expires_at) VALUES (gen_random_uuid(), $1, '\x01', now() - interval '10 days'), (gen_random_uuid(), $1, '\x02', now() + interval '10 days')`, u)
	p.Exec(ctx, `INSERT INTO reports (id, reporter_id, target_type, target_id, reason, status, resolved_at) VALUES
		(gen_random_uuid(), $1, 'idea', gen_random_uuid(), 'spam', 'resolved', now() - interval '100 days'),
		(gen_random_uuid(), $1, 'idea', gen_random_uuid(), 'spam', 'resolved', now() - interval '10 days'),
		(gen_random_uuid(), $1, 'idea', gen_random_uuid(), 'spam', 'open', NULL)`, u)
	outbox.Add(ctx, p, outbox.Event{Type: "x", AggregateType: "t", AggregateID: u})
	p.Exec(ctx, `UPDATE outbox_events SET published_at = now() - interval '8 days'`)
	outbox.Add(ctx, p, outbox.Event{Type: "y", AggregateType: "t", AggregateID: u})
	for _, n := range []string{"CleanupSessions", "CleanupReports", "OutboxCleanup", "RegistryReconcile"} {
		if _, err := env.Server.Jobs.RunTask(ctx, n); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
	count := func(sql string) int { var n int; p.QueryRow(ctx, sql).Scan(&n); return n }
	if count(`SELECT count(*) FROM sessions`) != 1 || count(`SELECT count(*) FROM reports`) != 2 || count(`SELECT count(*) FROM outbox_events`) != 1 {
		t.Errorf("sessions=%d reports=%d outbox=%d", count(`SELECT count(*) FROM sessions`), count(`SELECT count(*) FROM reports`), count(`SELECT count(*) FROM outbox_events`))
	}
	if count(`SELECT count(*) FROM resources WHERE id IS NOT NULL`) < 1 {
		t.Error("registry reconcile ran")
	}
}

func TestStartStopsWithContext(t *testing.T) {
	env := servertest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	env.Server.Jobs.Start(ctx, &wg)
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("jobs must stop with the context")
	}
}

func TestMetricsEndpoint(t *testing.T) {
	env := servertest.New(t)
	c := env.Client()
	c.Get("/api/v1/users/nobody")
	c.Get("/api/v1/ideas")
	env.Server.Pool.Exec(context.Background(), `INSERT INTO i18n_releases (version, manifest, status) VALUES ('mv1','{}','active')`)
	env.Server.Pool.Exec(context.Background(), `INSERT INTO i18n_active (id, version) VALUES (1,'mv1')`)
	outbox.Add(context.Background(), env.Server.Pool, outbox.Event{Type: "z", AggregateType: "t", AggregateID: testutil.InsertUser(t, env.Server.Pool, "m")})
	rec := httptest.NewRecorder()
	metrics.Handler(env.Server.Pool, env.Server.Redis).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	for _, want := range []string{`http_requests_total{route="/api/v1/users/{slug}",status="404"}`, "http_request_duration_seconds_bucket", "sse_connections", "db_pool_in_use", "outbox_unpublished 1", `i18n_active_version_info{version="mv1"} 1`, "outbox_oldest_unpublished_seconds"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics must contain %q", want)
		}
	}
}

func TestRetentionJobs(t *testing.T) {
	env := servertest.New(t)
	ctx := context.Background()
	u := env.LoginAs("ret@example.com").UserID()
	exec := func(q string, args ...any) {
		if _, err := env.Server.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO reports (id, reporter_id, target_type, target_id, reason, details, snapshot, status, created_at)
		VALUES (gen_random_uuid(), $1, 'message', gen_random_uuid(), 'abuse', 'private words', '[{"body":"secret"}]', 'open', now() - interval '200 days')`, u)
	exec(`INSERT INTO reports (id, reporter_id, target_type, target_id, reason, status, resolved_at, created_at)
		VALUES (gen_random_uuid(), $1, 'user', gen_random_uuid(), 'spam', 'resolved', now() - interval '100 days', now() - interval '120 days')`, u)
	exec(`INSERT INTO audit_events (id, action, created_at) VALUES (gen_random_uuid(), 'old.event', now() - interval '91 days')`)
	exec(`INSERT INTO audit_events (id, action) VALUES (gen_random_uuid(), 'fresh.event')`)
	for _, n := range []string{"CleanupReports", "CleanupAudit"} {
		if ran, err := env.Server.Jobs.RunTask(ctx, n); err != nil || !ran {
			t.Fatalf("%s: %v %v", n, ran, err)
		}
	}
	var open, closed, details, old, fresh int
	env.Server.Pool.QueryRow(ctx, `SELECT count(*) FROM reports WHERE status='open'`).Scan(&open)
	env.Server.Pool.QueryRow(ctx, `SELECT count(*) FROM reports WHERE status='resolved'`).Scan(&closed)
	env.Server.Pool.QueryRow(ctx, `SELECT count(*) FROM reports WHERE status='open' AND (snapshot IS NOT NULL OR details <> '' OR reporter_id IS NOT NULL)`).Scan(&details)
	env.Server.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='old.event'`).Scan(&old)
	env.Server.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='fresh.event'`).Scan(&fresh)
	if open != 1 || details != 0 || closed != 0 || old != 0 || fresh != 1 {
		t.Fatalf("open=%d personal=%d closed=%d old=%d fresh=%d", open, details, closed, old, fresh)
	}
}
