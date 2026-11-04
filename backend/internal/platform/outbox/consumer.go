package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// StreamEvent is a decoded stream entry.
type StreamEvent struct {
	ID            string // stream entry id
	OutboxID      int64
	Type          string
	AggregateType string
	AggregateID   uuid.UUID
	Payload       map[string]any
}

// Handler must be idempotent: events are delivered at least once.
type Handler func(ctx context.Context, ev StreamEvent) error

// Consumer reads Stream through a consumer group. Event types without a handler are acknowledged and skipped.
type Consumer struct {
	RDB           *redis.Client
	Stream        string
	Group         string
	Name          string
	Handlers      map[string]Handler
	ClaimIdle     time.Duration // pending entries idle longer than this are re-delivered (default 60s)
	MaxDeliveries int64         // after this many deliveries an entry goes to events:dead (default 5)
	Block         time.Duration // XREADGROUP block time (default 5s)
}

func (c *Consumer) defaults() {
	if c.Stream == "" {
		c.Stream = StreamMain
	}
	if c.ClaimIdle == 0 {
		c.ClaimIdle = 60 * time.Second
	}
	if c.MaxDeliveries == 0 {
		c.MaxDeliveries = 5
	}
	if c.Block == 0 {
		c.Block = 5 * time.Second
	}
}

func decode(m redis.XMessage) (StreamEvent, error) {
	ev := StreamEvent{ID: m.ID}
	str := func(k string) string { s, _ := m.Values[k].(string); return s }
	ev.Type, ev.AggregateType = str("type"), str("aggregate_type")
	ev.OutboxID, _ = strconv.ParseInt(str("id"), 10, 64)
	id, err := uuid.Parse(str("aggregate_id"))
	if err != nil {
		return ev, err
	}
	ev.AggregateID = id
	if p := str("payload"); p != "" {
		if err := json.Unmarshal([]byte(p), &ev.Payload); err != nil {
			return ev, err
		}
	}
	return ev, nil
}

func (c *Consumer) handle(ctx context.Context, m redis.XMessage) {
	ev, err := decode(m)
	if err != nil { // poison message: cannot ever succeed
		slog.Error("outbox consumer: undecodable entry", "group", c.Group, "id", m.ID, "err", err)
		c.dead(ctx, m, "undecodable: "+err.Error())
		return
	}
	h, ok := c.Handlers[ev.Type]
	if !ok {
		_ = c.RDB.XAck(ctx, c.Stream, c.Group, m.ID).Err()
		return
	}
	if err := h(ctx, ev); err != nil {
		slog.Warn("outbox consumer: handler failed", "group", c.Group, "type", ev.Type, "id", m.ID, "err", err)
		return // stays pending, will be re-delivered
	}
	_ = c.RDB.XAck(ctx, c.Stream, c.Group, m.ID).Err()
}

func (c *Consumer) dead(ctx context.Context, m redis.XMessage, reason string) {
	vals := map[string]any{"reason": reason, "group": c.Group, "stream_id": m.ID}
	for k, v := range m.Values {
		vals[k] = v
	}
	_ = c.RDB.XAdd(ctx, &redis.XAddArgs{Stream: StreamDead, MaxLen: 10000, Approx: true, Values: vals}).Err()
	_ = c.RDB.XAck(ctx, c.Stream, c.Group, m.ID).Err()
}

// ReclaimOnce re-delivers stale pending entries to this consumer; entries delivered too many times go to events:dead.
func (c *Consumer) ReclaimOnce(ctx context.Context) error {
	c.defaults()
	pend, err := c.RDB.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: c.Stream, Group: c.Group, Start: "-", End: "+", Count: 100, Idle: c.ClaimIdle}).Result()
	if err != nil {
		return err
	}
	for _, p := range pend {
		msgs, err := c.RDB.XClaim(ctx, &redis.XClaimArgs{Stream: c.Stream, Group: c.Group, Consumer: c.Name, MinIdle: c.ClaimIdle, Messages: []string{p.ID}}).Result()
		if err != nil || len(msgs) == 0 {
			continue
		}
		if p.RetryCount >= c.MaxDeliveries {
			c.dead(ctx, msgs[0], "max deliveries exceeded")
			continue
		}
		c.handle(ctx, msgs[0])
	}
	return nil
}

// ReadOnce reads one batch of new entries (blocking up to Block) and handles them.
func (c *Consumer) ReadOnce(ctx context.Context) error {
	c.defaults()
	res, err := c.RDB.XReadGroup(ctx, &redis.XReadGroupArgs{Group: c.Group, Consumer: c.Name, Streams: []string{c.Stream, ">"}, Count: 50, Block: c.Block}).Result()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, s := range res {
		for _, m := range s.Messages {
			c.handle(ctx, m)
		}
	}
	return nil
}

// Init creates the consumer group (from the beginning of the stream) if it does not exist.
func (c *Consumer) Init(ctx context.Context) error {
	c.defaults()
	err := c.RDB.XGroupCreateMkStream(ctx, c.Stream, c.Group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

// Run processes events until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) {
	c.defaults()
	for ctx.Err() == nil {
		if err := c.Init(ctx); err != nil {
			slog.Warn("outbox consumer: init failed", "group", c.Group, "err", err)
			sleep(ctx, 2*time.Second)
			continue
		}
		break
	}
	lastReclaim := time.Now()
	for ctx.Err() == nil {
		if time.Since(lastReclaim) > c.ClaimIdle/2 {
			if err := c.ReclaimOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("outbox consumer: reclaim failed", "group", c.Group, "err", err)
			}
			lastReclaim = time.Now()
		}
		if err := c.ReadOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("outbox consumer: read failed", "group", c.Group, "err", err)
			// Redis lost its data (restart without persistence, FLUSHALL): recreate the stream and the group.
			// Events that were only in the lost stream are gone; clients resynchronise from PostgreSQL on reconnect.
			if strings.Contains(err.Error(), "NOGROUP") {
				if ierr := c.Init(ctx); ierr != nil {
					slog.Warn("outbox consumer: re-init failed", "group", c.Group, "err", ierr)
				}
			}
			sleep(ctx, 2*time.Second)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// Pending returns the number of unacknowledged entries of the group.
func Pending(ctx context.Context, rdb *redis.Client, stream, group string) (int64, error) {
	p, err := rdb.XPending(ctx, stream, group).Result()
	if err != nil {
		return 0, err
	}
	return p.Count, nil
}
