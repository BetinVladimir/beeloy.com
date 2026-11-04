package contentsync

import (
	"context"
	"encoding/json"
	"time"

	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/ids"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	Pool  *pgxpool.Pool
	Audit audit.Service
}

// TryLock takes a session-level advisory lock on a dedicated connection; release must be called.
func (s *Store) TryLock(ctx context.Context, key int64) (release func(), ok bool, err error) {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&ok); err != nil || !ok {
		conn.Release()
		return nil, false, err
	}
	return func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
		conn.Release()
	}, true, nil
}

func (s *Store) GetRelease(ctx context.Context, version string) (string, bool, error) {
	var st string
	err := s.Pool.QueryRow(ctx, `SELECT status FROM i18n_releases WHERE version = $1`, version).Scan(&st)
	if db.IsNoRows(err) {
		return "", false, nil
	}
	return st, err == nil, err
}

func (s *Store) CurrentActive(ctx context.Context) (string, bool, error) {
	var v string
	err := s.Pool.QueryRow(ctx, `SELECT version FROM i18n_active WHERE id = 1`).Scan(&v)
	if db.IsNoRows(err) {
		return "", false, nil
	}
	return v, err == nil, err
}

// DeleteUnusable removes a failed/staged release so the version can be tried again.
func (s *Store) DeleteUnusable(ctx context.Context, version string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM i18n_releases WHERE version = $1 AND status IN ('failed','staged')`, version)
	return err
}

// StageRelease stores the release and all its bundles in one transaction (not yet visible to readers).
func (s *Store) StageRelease(ctx context.Context, m Manifest, raw []byte, bundles map[string]Bundle) error {
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO i18n_releases (version, manifest, status) VALUES ($1,$2,'staged')`, m.Version, raw); err != nil {
			return err
		}
		for _, b := range bundles {
			content, err := json.Marshal(b.Messages)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO i18n_bundles (version, locale, content, sha256) VALUES ($1,$2,$3,$4)`, m.Version, b.Locale, content, b.SHA256); err != nil {
				return err
			}
		}
		return nil
	})
}

// Activate switches readers to version in one transaction.
func (s *Store) Activate(ctx context.Context, version string) error {
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE i18n_releases SET status = 'superseded' WHERE status = 'active' AND version <> $1`, version); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE i18n_releases SET status = 'active', activated_at = now(), error = NULL WHERE version = $1`, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		if _, err := tx.Exec(ctx, `INSERT INTO i18n_active (id, version) VALUES (1,$1) ON CONFLICT (id) DO UPDATE SET version = EXCLUDED.version`, version); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Input{Action: audit.ActionI18nActivate, TargetType: "i18n_release", TargetID: version})
	})
}

// MarkFailed records a failed attempt (creating a placeholder release row if needed).
func (s *Store) MarkFailed(ctx context.Context, version, errText string) error {
	if len(errText) > 4000 {
		errText = errText[:4000]
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO i18n_releases (version, manifest, status, error) VALUES ($1,'{}','failed',$2)
		ON CONFLICT (version) DO UPDATE SET status = 'failed', error = EXCLUDED.error WHERE i18n_releases.status IN ('failed','staged')`, version, errText)
	return err
}

// PruneOld keeps the newest `keep` releases (the active one is never deleted).
func (s *Store) PruneOld(ctx context.Context, keep int) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM i18n_releases WHERE status <> 'active' AND version NOT IN (
		SELECT version FROM i18n_releases ORDER BY created_at DESC, version DESC LIMIT $1)`, keep)
	return err
}

func (s *Store) StartRun(ctx context.Context) (uuid.UUID, error) {
	id := ids.New()
	_, err := s.Pool.Exec(ctx, `INSERT INTO sync_runs (id) VALUES ($1)`, id)
	return id, err
}

func (s *Store) FinishRun(ctx context.Context, id uuid.UUID, res Result, version, errText string) error {
	if len(errText) > 4000 {
		errText = errText[:4000]
	}
	_, err := s.Pool.Exec(ctx, `UPDATE sync_runs SET finished_at = now(), result = $2, version = NULLIF($3,''), error = NULLIF($4,'') WHERE id = $1`, id, res, version, errText)
	return err
}

type Run struct {
	ID         uuid.UUID  `json:"id"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Result     *string    `json:"result"`
	Version    *string    `json:"version"`
	Error      *string    `json:"error"`
}

func (s *Store) LastRuns(ctx context.Context, n int) ([]Run, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, started_at, finished_at, result, version, error FROM sync_runs ORDER BY started_at DESC LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.StartedAt, &r.FinishedAt, &r.Result, &r.Version, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
