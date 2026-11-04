// Package ratelimit implements a Redis fixed-window limiter and HTTP middleware.
package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/httpx"
	"github.com/redis/go-redis/v9"
)

type Limiter struct{ rdb *redis.Client }

func New(rdb *redis.Client) *Limiter { return &Limiter{rdb: rdb} }

// Rule is a named limit: at most Limit events per Window for one identifier.
type Rule struct {
	Name   string
	Limit  int
	Window time.Duration
}

var incr = redis.NewScript(`
local c = redis.call('INCR', KEYS[1])
if c == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return {c, redis.call('PTTL', KEYS[1])}`)

// Allow counts one event for id. allowed=false when the count exceeds Limit; retryAfter is the remaining window.
func (l *Limiter) Allow(ctx context.Context, rule Rule, id string) (allowed bool, retryAfter time.Duration, err error) {
	res, err := incr.Run(ctx, l.rdb, []string{"rl:" + rule.Name + ":" + id}, rule.Window.Milliseconds()).Int64Slice()
	if err != nil {
		return false, 0, err
	}
	if len(res) != 2 {
		return false, 0, fmt.Errorf("ratelimit: bad script result")
	}
	if res[0] <= int64(rule.Limit) {
		return true, 0, nil
	}
	ttl := time.Duration(res[1]) * time.Millisecond
	if ttl < time.Second {
		ttl = time.Second
	}
	return false, ttl, nil
}

// Check is Allow translated to API errors: rate_limited (429) or unavailable (503) when failClosed and Redis fails.
// With failClosed=false a Redis failure lets the request pass (a warning is logged).
func (l *Limiter) Check(ctx context.Context, rule Rule, id string, failClosed bool) error {
	ok, retry, err := l.Allow(ctx, rule, id)
	if err != nil {
		if failClosed {
			return apperr.ErrUnavailable.Wrap(err)
		}
		slog.Warn("ratelimit: redis error, failing open", "rule", rule.Name, "err", err)
		return nil
	}
	if !ok {
		return apperr.RateLimited(rule.Name, int((retry+time.Second-1)/time.Second))
	}
	return nil
}

// ByIP keys requests by client IP.
func ByIP(r *http.Request) string { return httpx.ClientIP(r).String() }

// Middleware limits requests per key (see ByIP; handlers needing a user key use Check directly).
func (l *Limiter) Middleware(rule Rule, keyFn func(*http.Request) string, failClosed bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := l.Check(r.Context(), rule, keyFn(r), failClosed); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
