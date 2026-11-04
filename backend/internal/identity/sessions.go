package identity

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/crypto"
	"beeloy.com/backend/internal/platform/ids"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const CookieName = "bl_session"

type SessionConfig struct {
	TTL        time.Duration // 30 days
	RenewAfter time.Duration // 24 hours
	CookieName string
	Secure     bool
}

func DefaultSessionConfig(secure bool) SessionConfig {
	return SessionConfig{TTL: 30 * 24 * time.Hour, RenewAfter: 24 * time.Hour, CookieName: CookieName, Secure: secure}
}

type SessionService struct {
	Pool *pgxpool.Pool
	Repo Repo
	ACL  acl.Repo
	Cfg  SessionConfig
}

// Create stores SHA-256(token) and returns the raw token. This is the only place the raw token exists.
func (s *SessionService) Create(ctx context.Context, userID uuid.UUID, ua string, ip netip.Addr) (string, time.Time, error) {
	token, err := crypto.RandomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	exp := time.Now().Add(s.Cfg.TTL)
	sess := Session{ID: ids.New(), UserID: userID, TokenHash: crypto.SHA256([]byte(token)), ExpiresAt: exp}
	if err := s.Repo.InsertSession(ctx, s.Pool, sess, ua, ip); err != nil {
		return "", time.Time{}, err
	}
	return token, exp, nil
}

// Resolve authenticates a raw token. blocked/deleted users are treated as unauthenticated;
// pending_deletion users stay authenticated (they may restore the account). Bindings are read from the DB every time.
func (s *SessionService) Resolve(ctx context.Context, token string) (acl.Subject, Session, error) {
	if token == "" || len(token) > 200 {
		return acl.Subject{}, Session{}, apperr.ErrUnauthorized
	}
	sess, err := s.Repo.GetSessionByHash(ctx, s.Pool, crypto.SHA256([]byte(token)))
	if err != nil {
		return acl.Subject{}, Session{}, apperr.ErrUnauthorized
	}
	u, err := s.Repo.GetUserByID(ctx, s.Pool, sess.UserID)
	if err != nil {
		return acl.Subject{}, Session{}, apperr.ErrUnauthorized
	}
	if u.Status == StatusBlocked || u.Status == StatusDeleted {
		return acl.Subject{}, Session{}, apperr.ErrUnauthorized
	}
	bs, err := s.ACL.ListBindings(ctx, s.Pool, u.ID)
	if err != nil {
		return acl.Subject{}, Session{}, err
	}
	if time.Since(sess.LastSeenAt) > s.Cfg.RenewAfter {
		_ = s.Repo.TouchSession(ctx, s.Pool, sess.ID, time.Now().Add(s.Cfg.TTL))
	}
	return acl.Subject{UserID: u.ID, Status: string(u.Status), Bindings: bs}, sess, nil
}

func (s *SessionService) Revoke(ctx context.Context, sessionID uuid.UUID) error {
	return s.Repo.RevokeSession(ctx, s.Pool, sessionID)
}

func (s *SessionService) RevokeAll(ctx context.Context, userID uuid.UUID) error {
	return s.Repo.RevokeAllUserSessions(ctx, s.Pool, userID)
}

// SetCookie writes the host-only session cookie (HttpOnly, SameSite=Lax, no Domain).
func SetCookie(w http.ResponseWriter, cfg SessionConfig, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: cfg.CookieName, Value: token, Path: "/", Expires: expires, HttpOnly: true, Secure: cfg.Secure, SameSite: http.SameSiteLaxMode})
}

func ClearCookie(w http.ResponseWriter, cfg SessionConfig) {
	http.SetCookie(w, &http.Cookie{Name: cfg.CookieName, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(0, 0), HttpOnly: true, Secure: cfg.Secure, SameSite: http.SameSiteLaxMode})
}
