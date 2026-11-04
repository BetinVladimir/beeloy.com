// Package maintenance implements the write freeze used while a consistent PostgreSQL+S3 backup is taken.
// The operator (backup.sh) sets the Redis key "maintenance" with a TTL; writers check it. If Redis cannot be read the
// freeze is not applied, because blocking all writes on a Redis blip would be worse than a skipped freeze: the backup
// script verifies the S3 inventory before and after copying and refuses an inconsistent set.
package maintenance

import (
	"context"
	"net/http"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/httpx"
	"github.com/redis/go-redis/v9"
)

const Key = "maintenance"

// On reports whether the freeze flag is set.
func On(ctx context.Context, rdb *redis.Client) bool {
	n, err := rdb.Exists(ctx, Key).Result()
	return err == nil && n > 0
}

// Middleware answers 503 to every state-changing request while the freeze is on. Reads and SSE stay available.
func Middleware(rdb *redis.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if On(r.Context(), rdb) {
				w.Header().Set("Retry-After", "300")
				httpx.WriteError(w, r, apperr.ErrUnavailable.WithMessage("maintenance"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
