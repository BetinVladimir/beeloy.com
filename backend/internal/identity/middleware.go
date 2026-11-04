package identity

import (
	"context"
	"net/http"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/httpx"
)

type sessKey struct{}

// SessionFrom returns the current session (set by LoadSession).
func SessionFrom(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessKey{}).(Session)
	return s, ok
}

// UserIDForLog is passed to httpx.AccessLog.
func UserIDForLog(ctx context.Context) string {
	if s, ok := acl.SubjectFrom(ctx); ok {
		return s.UserID.String()
	}
	return ""
}

// LoadSession authenticates the cookie if present. A missing or invalid cookie is not an error here.
func LoadSession(ss *SessionService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie(ss.Cfg.CookieName); err == nil && c.Value != "" {
				if subj, sess, err := ss.Resolve(r.Context(), c.Value); err == nil {
					ctx := acl.WithSubject(r.Context(), subj)
					ctx = context.WithValue(ctx, sessKey{}, sess)
					r = r.WithContext(ctx)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth answers 401 without a valid session.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := acl.SubjectFrom(r.Context()); !ok {
			httpx.WriteError(w, r, apperr.ErrUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireActive answers 403 for accounts that are not active (e.g. pending deletion).
func RequireActive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := acl.SubjectFrom(r.Context())
		if !ok {
			httpx.WriteError(w, r, apperr.ErrUnauthorized)
			return
		}
		if s.Status != string(StatusActive) {
			httpx.WriteError(w, r, apperr.Forbidden("account_not_active"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// MustSubject is for handlers behind RequireAuth.
func MustSubject(r *http.Request) acl.Subject {
	s, _ := acl.SubjectFrom(r.Context())
	return s
}
