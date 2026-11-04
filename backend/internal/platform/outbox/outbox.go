// Package outbox implements the transactional outbox: events are written in the same transaction as the data change,
// a relay moves them to a Redis Stream, and consumer groups process them at least once.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"beeloy.com/backend/internal/platform/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	StreamMain   = "events:main"
	StreamDead   = "events:dead"
	streamMaxLen = 100000
	lockRelay    = 7004
)

// Event payloads carry identifiers and numbers only: never e-mail, message text or tokens.
type Event struct {
	Type          string
	AggregateType string
	AggregateID   uuid.UUID
	Payload       map[string]any
}

// Add inserts the event through q (the caller's transaction). A rollback removes the event too.
func Add(ctx context.Context, q db.DBTX, e Event) error {
	p := e.Payload
	if p == nil {
		p = map[string]any{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO outbox_events (event_type, aggregate_type, aggregate_id, payload) VALUES ($1,$2,$3,$4)`,
		e.Type, e.AggregateType, e.AggregateID, b)
	return err
}

// Relay publishes unpublished outbox rows to the Redis Stream in id order.
type Relay struct {
	Pool   *pgxpool.Pool
	RDB    *redis.Client
	Stream string
	Batch  int
	Tick   time.Duration
	kick   chan struct{}
}

func NewRelay(pool *pgxpool.Pool, rdb *redis.Client) *Relay {
	return &Relay{Pool: pool, RDB: rdb, Stream: StreamMain, Batch: 100, Tick: 200 * time.Millisecond, kick: make(chan struct{}, 1)}
}

// Kick wakes the relay immediately (call after a commit that wrote events). It never blocks.
func (r *Relay) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// FlushOnce publishes one batch and returns how many events were published.
func (r *Relay) FlushOnce(ctx context.Context) (int, error) {
	published := 0
	var failID int64
	err := db.WithTx(ctx, r.Pool, func(tx pgx.Tx) error {
		var got bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, lockRelay).Scan(&got); err != nil {
			return err
		}
		if !got {
			return nil // another replica is relaying
		}
		rows, err := tx.Query(ctx, `SELECT id, event_type, aggregate_type, aggregate_id, payload FROM outbox_events
			WHERE published_at IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, r.Batch)
		if err != nil {
			return err
		}
		type row struct {
			id           int64
			typ, aggType string
			aggID        uuid.UUID
			payload      []byte
		}
		var batch []row
		for rows.Next() {
			var x row
			if err := rows.Scan(&x.id, &x.typ, &x.aggType, &x.aggID, &x.payload); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var ids []int64
		for _, x := range batch {
			err := r.RDB.XAdd(ctx, &redis.XAddArgs{Stream: r.Stream, MaxLen: streamMaxLen, Approx: true, Values: map[string]any{
				"id": strconv.FormatInt(x.id, 10), "type": x.typ, "aggregate_type": x.aggType, "aggregate_id": x.aggID.String(), "payload": string(x.payload),
			}}).Err()
			if err != nil {
				failID = x.id
				return fmt.Errorf("outbox: xadd %d: %w", x.id, err)
			}
			ids = append(ids, x.id)
		}
		if len(ids) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox_events SET published_at = now() WHERE id = ANY($1)`, ids); err != nil {
			return err
		}
		published = len(ids)
		return nil
	})
	if err != nil {
		if failID != 0 {
			_, _ = r.Pool.Exec(context.Background(), `UPDATE outbox_events SET attempts = attempts + 1, last_error = $2 WHERE id = $1`, failID, truncate(err.Error(), 300))
		}
		return 0, err
	}
	return published, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Run loops until ctx is cancelled: on Redis failure it backs off 1s…30s while events accumulate in PostgreSQL.
func (r *Relay) Run(ctx context.Context) {
	t := time.NewTicker(r.Tick)
	defer t.Stop()
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.kick:
		}
		for {
			n, err := r.FlushOnce(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Warn("outbox relay failed", "err", err, "retry_in", backoff)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < 30*time.Second {
					backoff *= 2
				}
				break
			}
			backoff = time.Second
			if n < r.Batch {
				break
			}
		}
	}
}

// Cleanup deletes published events older than the retention period.
func Cleanup(ctx context.Context, q db.DBTX, olderThan time.Duration) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM outbox_events WHERE published_at < now() - make_interval(secs => $1)`, olderThan.Seconds())
	return tag.RowsAffected(), err
}

// Status describes the outbox backlog (CLI, metrics).
type Status struct {
	Unpublished int64
	OldestAge   time.Duration
	StreamLen   int64
	DeadLen     int64
}

func GetStatus(ctx context.Context, q db.DBTX, rdb *redis.Client) (Status, error) {
	var st Status
	var age *float64
	if err := q.QueryRow(ctx, `SELECT count(*), EXTRACT(EPOCH FROM now() - min(created_at)) FROM outbox_events WHERE published_at IS NULL`).Scan(&st.Unpublished, &age); err != nil {
		return st, err
	}
	if age != nil {
		st.OldestAge = time.Duration(*age * float64(time.Second))
	}
	st.StreamLen, _ = rdb.XLen(ctx, StreamMain).Result()
	st.DeadLen, _ = rdb.XLen(ctx, StreamDead).Result()
	return st, nil
}
