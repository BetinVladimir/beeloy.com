package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/crypto"
	"beeloy.com/backend/internal/platform/ids"
)

// statusWriter records the status and keeps Flush working (needed by SSE).
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(c int) {
	if !s.wrote {
		s.status, s.wrote = c, true
	}
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status, s.wrote = http.StatusOK, true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// RequestID reads or creates X-Request-Id and puts it in the context and response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 64 {
			id = ids.New().String()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyRequestID, id)))
	})
}

// Recoverer turns panics into 500 "internal".
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				slog.Error("panic", "request_id", RequestIDFrom(r.Context()), "panic", p, "stack", string(debug.Stack()))
				WriteError(w, r, apperr.ErrInternal)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// UserIDFunc extracts a user id for logs (set by the auth layer); may be nil.
type UserIDFunc func(ctx context.Context) string

// AccessLog logs method, path (without query), status, duration, request id, user id. Bodies are never logged.
func AccessLog(userID UserIDFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(sw, r)
			attrs := []any{"method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds(), "request_id", RequestIDFrom(r.Context())}
			if userID != nil {
				if u := userID(r.Context()); u != "" {
					attrs = append(attrs, "user_id", u)
				}
			}
			slog.Info("http", attrs...)
		})
	}
}

// RealIPConfig describes which proxies are trusted and the shared secret of the Next.js proxy.
type RealIPConfig struct {
	TrustedCIDRs []netip.Prefix
	SharedSecret string
}

func (c RealIPConfig) trusted(a netip.Addr) bool {
	for _, p := range c.TrustedCIDRs {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// RealIP resolves the client IP: X-Real-Client-IP (when X-Proxy-Secret matches and the peer is trusted),
// else CF-Connecting-IP (trusted peer), else the TCP peer.
func RealIP(cfg RealIPConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer, _ := netip.ParseAddrPort(r.RemoteAddr)
			ip := peer.Addr().Unmap()
			ingressOK := false
			if cfg.trusted(ip) {
				if cfg.SharedSecret != "" && crypto.ConstTimeEqual([]byte(r.Header.Get("X-Proxy-Secret")), []byte(cfg.SharedSecret)) {
					ingressOK = true
					if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-Client-IP"))); err == nil {
						ip = a.Unmap()
					}
				} else if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); err == nil {
					ip = a.Unmap()
				}
			}
			r.Header.Del("X-Proxy-Secret")
			r.Header.Del("X-Real-Client-IP")
			ctx := context.WithValue(r.Context(), keyClientIP, ip)
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, keyIngressOK, ingressOK)))
		})
	}
}

// SecurityHeaders sets safe defaults; handlers with their own Cache-Control override the default.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// MaxBody limits request bodies.
func MaxBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRF protects unsafe methods: Origin must be allowed and X-Requested-With must equal "beeloy".
func CSRF(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := map[string]struct{}{}
	for _, o := range allowedOrigins {
		allowed[o] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := allowed[r.Header.Get("Origin")]; !ok || r.Header.Get("X-Requested-With") != "beeloy" {
				WriteError(w, r, apperr.ErrForbidden.WithMessage("csrf check failed"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// publicIngressException lists the only paths readable without the Worker secret (GET dictionaries and categories).
func publicIngressException(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	p := r.URL.Path
	return p == "/api/v1/categories" || p == "/api/v1/i18n/meta" ||
		(strings.HasPrefix(p, "/api/v1/i18n/") && !strings.Contains(strings.TrimPrefix(p, "/api/v1/i18n/"), "/"))
}

// RequireTrustedIngress makes the API reachable only through Caddy (peer in TRUSTED_PROXY_CIDRS) carrying the
// Worker secret. Only the listed public GET paths are exempt from the secret, never from the peer check.
func RequireTrustedIngress(cfg RealIPConfig, enforce bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !enforce {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, _ := r.Context().Value(keyIngressOK).(bool)
			if !ok && publicIngressException(r) {
				if peer, err := netip.ParseAddrPort(r.RemoteAddr); err == nil && cfg.trusted(peer.Addr().Unmap()) {
					ok = true
				}
			}
			if !ok {
				WriteError(w, r, apperr.ErrForbidden.WithMessage("untrusted ingress"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
