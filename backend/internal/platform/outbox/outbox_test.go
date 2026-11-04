package outbox_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/ids"
	"beeloy.com/backend/internal/platform/outbox"
	"beeloy.com/backend/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

func ev(typ string) outbox.Event {
	return outbox.Event{Type: typ, AggregateType: "test", AggregateID: ids.New(), Payload: map[string]any{"n": 1}}
}

func TestRollbackLeavesNoEvent(t *testing.T) {
	pool := testutil.PG(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if err := outbox.Add(ctx, tx, ev("x.created")); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&n)
	if n != 0 {
		t.Fatal("rolled back event must not exist")
	}
}

func TestRelayPublishesInOrderAndMarksPublished(t *testing.T) {
	pool, rdb := testutil.PG(t), testutil.Redis(t)
	ctx := context.Background()
	for _, ty := range []string{"a", "b", "c"} {
		if err := outbox.Add(ctx, pool, ev(ty)); err != nil {
			t.Fatal(err)
		}
	}
	r := outbox.NewRelay(pool, rdb)
	n, err := r.FlushOnce(ctx)
	if err != nil || n != 3 {
		t.Fatalf("flush: %d %v", n, err)
	}
	msgs, _ := rdb.XRange(ctx, outbox.StreamMain, "-", "+").Result()
	if len(msgs) != 3 || msgs[0].Values["type"] != "a" || msgs[2].Values["type"] != "c" {
		t.Fatalf("order: %v", msgs)
	}
	if n, _ := r.FlushOnce(ctx); n != 0 {
		t.Fatal("nothing left to publish")
	}
	st, _ := outbox.GetStatus(ctx, pool, rdb)
	if st.Unpublished != 0 || st.StreamLen != 3 {
		t.Fatalf("status: %+v", st)
	}
}

func TestRedisOutageKeepsEventsAndRecovers(t *testing.T) {
	pool, rdb := testutil.PG(t), testutil.Redis(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = outbox.Add(ctx, pool, ev("x"))
	}
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 200 * time.Millisecond})
	if _, err := outbox.NewRelay(pool, dead).FlushOnce(ctx); err == nil {
		t.Fatal("flush with dead Redis must fail")
	}
	var unpub, attempts int
	_ = pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE published_at IS NULL), COALESCE(max(attempts),0) FROM outbox_events`).Scan(&unpub, &attempts)
	if unpub != 5 || attempts < 1 {
		t.Fatalf("events must stay unpublished with attempts recorded: %d %d", unpub, attempts)
	}
	if n, err := outbox.NewRelay(pool, rdb).FlushOnce(ctx); err != nil || n != 5 {
		t.Fatalf("recovery: %d %v", n, err)
	}
}

func TestConcurrentRelaysNoDuplicates(t *testing.T) {
	pool, rdb := testutil.PG(t), testutil.Redis(t)
	ctx := context.Background()
	for i := 0; i < 300; i++ {
		_ = outbox.Add(ctx, pool, ev("x"))
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := outbox.NewRelay(pool, rdb)
			r.Batch = 50
			for k := 0; k < 8; k++ {
				_, _ = r.FlushOnce(ctx)
			}
		}()
	}
	wg.Wait()
	// a relay that finds the lock taken skips its turn, so drain what is left before counting
	for {
		n, err := outbox.NewRelay(pool, rdb).FlushOnce(ctx)
		if err != nil || n == 0 {
			break
		}
	}
	if n, _ := rdb.XLen(ctx, outbox.StreamMain).Result(); n != 300 {
		t.Fatalf("stream must hold each event exactly once, got %d", n)
	}
}

func TestConsumerAckRetryAndDeadLetter(t *testing.T) {
	pool, rdb := testutil.PG(t), testutil.Redis(t)
	ctx := context.Background()
	_ = outbox.Add(ctx, pool, ev("ok.event"))
	_ = outbox.Add(ctx, pool, ev("flaky.event"))
	_ = outbox.Add(ctx, pool, ev("poison.event"))
	_ = outbox.Add(ctx, pool, ev("unhandled.event"))
	if _, err := outbox.NewRelay(pool, rdb).FlushOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var okN, flakyN, poisonN atomic.Int32
	c := &outbox.Consumer{RDB: rdb, Group: "g1", Name: "c1", ClaimIdle: 50 * time.Millisecond, MaxDeliveries: 3, Block: 100 * time.Millisecond,
		Handlers: map[string]outbox.Handler{
			"ok.event": func(context.Context, outbox.StreamEvent) error { okN.Add(1); return nil },
			"flaky.event": func(context.Context, outbox.StreamEvent) error {
				if flakyN.Add(1) < 3 {
					return errors.New("temporary")
				}
				return nil
			},
			"poison.event": func(context.Context, outbox.StreamEvent) error { poisonN.Add(1); return errors.New("always") },
		}}
	if err := c.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.ReadOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		time.Sleep(80 * time.Millisecond)
		if err := c.ReclaimOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if okN.Load() != 1 {
		t.Errorf("ok handled %d times", okN.Load())
	}
	if flakyN.Load() != 3 {
		t.Errorf("flaky must succeed on 3rd delivery, got %d", flakyN.Load())
	}
	if dead, _ := rdb.XLen(ctx, outbox.StreamDead).Result(); dead != 1 {
		t.Errorf("poison must be dead-lettered once, got %d", dead)
	}
	if p, _ := outbox.Pending(ctx, rdb, outbox.StreamMain, "g1"); p != 0 {
		t.Errorf("nothing may stay pending, got %d", p)
	}
	if poisonN.Load() < 2 || poisonN.Load() > 3 {
		t.Errorf("poison deliveries: %d", poisonN.Load())
	}
}

func TestRunLoopDeliversAfterKick(t *testing.T) {
	pool, rdb := testutil.PG(t), testutil.Redis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got atomic.Int32
	c := &outbox.Consumer{RDB: rdb, Group: "g", Name: "n", Block: 100 * time.Millisecond,
		Handlers: map[string]outbox.Handler{"ping": func(context.Context, outbox.StreamEvent) error { got.Add(1); return nil }}}
	r := outbox.NewRelay(pool, rdb)
	go r.Run(ctx)
	go c.Run(ctx)
	_ = outbox.Add(ctx, pool, ev("ping"))
	r.Kick()
	deadline := time.Now().Add(5 * time.Second)
	for got.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got.Load() != 1 {
		t.Fatalf("event not delivered end-to-end: %d", got.Load())
	}
}

// Redis loses all its data while the consumer is running: the group is recreated and new events flow again.
func TestConsumerSurvivesRedisDataLoss(t *testing.T) {
	pool, rdb := testutil.PG(t), testutil.Redis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got atomic.Int32
	c := &outbox.Consumer{RDB: rdb, Group: "g-loss", Name: "n", Block: 100 * time.Millisecond,
		Handlers: map[string]outbox.Handler{"ping": func(context.Context, outbox.StreamEvent) error { got.Add(1); return nil }}}
	r := outbox.NewRelay(pool, rdb)
	go r.Run(ctx)
	go c.Run(ctx)
	wait := func(n int32) bool {
		deadline := time.Now().Add(10 * time.Second)
		for got.Load() < n && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		return got.Load() >= n
	}
	_ = outbox.Add(ctx, pool, ev("ping"))
	r.Kick()
	if !wait(1) {
		t.Fatal("first event")
	}
	if err := rdb.FlushAll(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	_ = outbox.Add(ctx, pool, ev("ping"))
	r.Kick()
	if !wait(2) {
		t.Fatalf("after Redis data loss the consumer must recover, got %d", got.Load())
	}
}
