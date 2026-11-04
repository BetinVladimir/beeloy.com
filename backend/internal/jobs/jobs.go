// Package jobs runs periodic maintenance tasks; every task is guarded by a PostgreSQL advisory lock so that
// only one API replica works on it at a time.
package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/platform/outbox"
	"beeloy.com/backend/internal/privacy"
	"beeloy.com/backend/internal/registry"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	lockPurge         int64 = 7001
	lockSessions      int64 = 7002
	lockReports       int64 = 7003
	lockOutboxCleanup int64 = 7005
	lockRegistry      int64 = 7006
	lockAudit         int64 = 7008 // 7007 is the transaction-level last-admin lock
)

type Task struct {
	Name     string
	Interval time.Duration
	Lock     int64
	Fn       func(ctx context.Context) error
}

type Runner struct {
	Pool    *pgxpool.Pool
	Privacy *privacy.Service
	Users   identity.Repo
}

func (r *Runner) Tasks() []Task {
	return []Task{
		{"PurgeDeletedAccounts", time.Hour, lockPurge, r.purge},
		{"CleanupSessions", 6 * time.Hour, lockSessions, func(ctx context.Context) error {
			_, err := r.Users.DeleteExpiredSessions(ctx, r.Pool, time.Now().Add(-7*24*time.Hour))
			return err
		}},
		{"CleanupReports", 24 * time.Hour, lockReports, func(ctx context.Context) error {
			// closed reports: gone 90 days after closing; open ones: text and snapshot are wiped after 180 days
			// (no legal hold is recorded in this MVP; one must pause this job before it applies)
			if _, err := r.Pool.Exec(ctx, `DELETE FROM reports WHERE status IN ('resolved','dismissed') AND COALESCE(resolved_at, created_at) < now() - interval '90 days'`); err != nil {
				return err
			}
			_, err := r.Pool.Exec(ctx, `UPDATE reports SET snapshot = NULL, details = '', reporter_id = NULL
				WHERE status = 'open' AND created_at < now() - interval '180 days' AND (snapshot IS NOT NULL OR details <> '' OR reporter_id IS NOT NULL)`)
			return err
		}},
		{"CleanupAudit", 24 * time.Hour, lockAudit, func(ctx context.Context) error {
			_, err := r.Pool.Exec(ctx, `DELETE FROM audit_events WHERE created_at < now() - interval '90 days'`)
			return err
		}},
		{"OutboxCleanup", 24 * time.Hour, lockOutboxCleanup, func(ctx context.Context) error {
			_, err := outbox.Cleanup(ctx, r.Pool, 7*24*time.Hour)
			return err
		}},
		{"RegistryReconcile", 24 * time.Hour, lockRegistry, func(ctx context.Context) error {
			n, err := registry.Reconcile(ctx, r.Pool)
			if n > 0 {
				slog.Info("registry reconciled", "fixed", n)
			}
			return err
		}},
	}
}

func (r *Runner) purge(ctx context.Context) error {
	ids, err := r.Privacy.DueForPurge(ctx, 50)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := r.Privacy.PurgeUser(ctx, id); err != nil {
			slog.Error("purge failed", "user", id, "err", err)
		}
	}
	return nil
}

// RunTask runs one task if no other replica holds its lock; ran reports whether it was executed here.
func (r *Runner) RunTask(ctx context.Context, name string) (ran bool, err error) {
	for _, t := range r.Tasks() {
		if t.Name != name {
			continue
		}
		conn, err := r.Pool.Acquire(ctx)
		if err != nil {
			return false, err
		}
		defer conn.Release()
		var got bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, t.Lock).Scan(&got); err != nil || !got {
			return false, err
		}
		defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, t.Lock)
		return true, t.Fn(ctx)
	}
	return false, nil
}

// Start launches one goroutine per task; errors are logged and never stop the process.
func (r *Runner) Start(ctx context.Context, wg *sync.WaitGroup) {
	for _, t := range r.Tasks() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
			for {
				if _, err := r.RunTask(ctx, t.Name); err != nil && ctx.Err() == nil {
					slog.Error("job failed", "job", t.Name, "err", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(t.Interval):
				}
			}
		}()
	}
}
