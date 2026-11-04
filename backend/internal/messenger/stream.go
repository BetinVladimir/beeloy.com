package messenger

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/platform/httpx"
	"beeloy.com/backend/internal/platform/metrics"
	"beeloy.com/backend/internal/platform/outbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	maxStreamsPerUser = 5
	heartbeat         = 25 * time.Second
	sessionRecheck    = 60 * time.Second
)

// Channel is the Redis Pub/Sub channel carrying live events of one user.
func Channel(userID uuid.UUID) string { return "msg:user:" + userID.String() }

type streamCounter struct {
	mu sync.Mutex
	n  map[uuid.UUID]int
}

func (c *streamCounter) acquire(id uuid.UUID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[uuid.UUID]int{}
	}
	if c.n[id] >= maxStreamsPerUser {
		return false
	}
	c.n[id]++
	return true
}

func (c *streamCounter) release(id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[id] <= 1 {
		delete(c.n, id)
		return
	}
	c.n[id]--
}

// events serves GET /events as Server-Sent Events. Missed events are recovered by the client via
// GET /conversations and GET /conversations/{id}/messages?after_seq=… after every (re)connect.
func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	subj := identity.MustSubject(r)
	rc := http.NewResponseController(w)
	if !h.streams.acquire(subj.UserID) {
		httpx.WriteError(w, r, apperr.RateLimited("too many streams", 30))
		return
	}
	defer h.streams.release(subj.UserID)
	metrics.SSEConnections.Inc()
	defer metrics.SSEConnections.Dec()

	ctx := r.Context()
	// subscribe BEFORE announcing readiness so that no event is lost between the two
	sub := h.Svc.RDB.Subscribe(ctx, Channel(subj.UserID))
	defer sub.Close()
	if _, err := sub.Receive(ctx); err != nil {
		httpx.WriteError(w, r, apperr.ErrUnavailable.Wrap(err))
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "private, no-store, no-transform")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "event: ready\ndata: {}\n\n")
	if err := rc.Flush(); err != nil {
		return
	}

	token := ""
	if c, err := r.Cookie(h.Sessions.Cfg.CookieName); err == nil {
		token = c.Value
	}
	ch := sub.Channel()
	hb := time.NewTicker(heartbeat)
	defer hb.Stop()
	recheck := time.NewTicker(sessionRecheck)
	defer recheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C:
			fmt.Fprint(w, ": ping\n\n")
		case <-recheck.C:
			if _, _, err := h.Sessions.Resolve(ctx, token); err != nil {
				return // signed out or blocked: close the stream
			}
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var head struct {
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(msg.Payload), &head) != nil || head.Type == "" {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", head.Type, msg.Payload)
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

// Realtime returns the outbox handlers of the "realtime" consumer group: events become Pub/Sub messages for SSE.
// Message text is read from PostgreSQL; the Stream only carries identifiers.
func Realtime(pool *pgxpool.Pool, rdb *redis.Client) map[string]outbox.Handler {
	repo := Repo{}
	pub := func(ctx context.Context, payload any, users ...uuid.UUID) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		seen := map[uuid.UUID]bool{}
		for _, u := range users {
			if seen[u] {
				continue
			}
			seen[u] = true
			if err := rdb.Publish(ctx, Channel(u), b).Err(); err != nil {
				return err
			}
		}
		return nil
	}
	uid := func(ev outbox.StreamEvent, k string) uuid.UUID {
		s, _ := ev.Payload[k].(string)
		id, _ := uuid.Parse(s)
		return id
	}
	return map[string]outbox.Handler{
		"message.created": func(ctx context.Context, ev outbox.StreamEvent) error {
			m, err := repo.GetMessage(ctx, pool, uid(ev, "message_id"))
			if err != nil {
				return err
			}
			return pub(ctx, map[string]any{"type": "message", "conversation_id": m.ConversationID, "message": m}, uid(ev, "recipient_id"), uid(ev, "sender_id"))
		},
		"message.read": func(ctx context.Context, ev outbox.StreamEvent) error {
			return pub(ctx, map[string]any{"type": "read", "conversation_id": uid(ev, "conversation_id"), "reader_id": uid(ev, "reader_id"), "read_seq": ev.Payload["read_seq"]},
				uid(ev, "peer_id"), uid(ev, "reader_id"))
		},
		"conversation.status_changed": func(ctx context.Context, ev outbox.StreamEvent) error {
			return pub(ctx, map[string]any{"type": "conversation", "conversation_id": uid(ev, "conversation_id"), "status": ev.Payload["status"], "actor_id": uid(ev, "actor_id")},
				uid(ev, "actor_id"), uid(ev, "peer_id"))
		},
	}
}
