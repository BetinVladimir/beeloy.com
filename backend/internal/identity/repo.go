package identity

import (
	"context"
	"net/netip"
	"time"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/db"
	"github.com/google/uuid"
)

// Repo contains only SQL for users, identities, sessions and agreements.
type Repo struct{}

const userCols = `u.id, u.nickname, u.bio, u.avatar_url, u.locale, u.status, u.profile_public, u.created_at, u.updated_at, COALESCE(s.slug,'')`
const userFrom = `FROM users u LEFT JOIN slugs s ON s.entity_type='user' AND s.entity_id=u.id AND s.is_current`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Nickname, &u.Bio, &u.AvatarURL, &u.Locale, &u.Status, &u.ProfilePublic, &u.CreatedAt, &u.UpdatedAt, &u.Slug)
	return u, err
}

func (Repo) CreateUser(ctx context.Context, q db.DBTX, u User) error {
	_, err := q.Exec(ctx, `INSERT INTO users (id, nickname, locale, status, profile_public) VALUES ($1,$2,$3,$4,$5)`,
		u.ID, u.Nickname, u.Locale, u.Status, u.ProfilePublic)
	return err
}

func (Repo) GetUserByID(ctx context.Context, q db.DBTX, id uuid.UUID) (User, error) {
	u, err := scanUser(q.QueryRow(ctx, `SELECT `+userCols+` `+userFrom+` WHERE u.id = $1`, id))
	if db.IsNoRows(err) {
		return u, apperr.ErrNotFound
	}
	return u, err
}

func (Repo) GetUserBySlug(ctx context.Context, q db.DBTX, slug string) (User, string, error) {
	var canonical string
	u, err := scanUser(q.QueryRow(ctx, `SELECT `+userCols+` FROM slugs o
		JOIN slugs s ON s.entity_type='user' AND s.entity_id=o.entity_id AND s.is_current
		JOIN users u ON u.id = o.entity_id
		WHERE o.entity_type='user' AND o.slug=$1`, slug))
	if db.IsNoRows(err) {
		return u, "", apperr.ErrNotFound
	}
	canonical = u.Slug
	return u, canonical, err
}

// UpdateProfile changes only non-nil fields.
func (Repo) UpdateProfile(ctx context.Context, q db.DBTX, id uuid.UUID, nickname, bio, locale *string, profilePublic *bool) error {
	_, err := q.Exec(ctx, `UPDATE users SET nickname = COALESCE($2, nickname), bio = COALESCE($3, bio), locale = COALESCE($4, locale),
		profile_public = COALESCE($5, profile_public), updated_at = now() WHERE id = $1`, id, nickname, bio, locale, profilePublic)
	return err
}

func (Repo) SetUserStatus(ctx context.Context, q db.DBTX, id uuid.UUID, st UserStatus, reason *string, deletionRequestedAt *time.Time) error {
	_, err := q.Exec(ctx, `UPDATE users SET status = $2, block_reason = $3, deletion_requested_at = $4, updated_at = now() WHERE id = $1`,
		id, st, reason, deletionRequestedAt)
	return err
}

func (Repo) FindIdentity(ctx context.Context, q db.DBTX, p Provider, subject string) (AuthIdentity, error) {
	var a AuthIdentity
	err := q.QueryRow(ctx, `SELECT id, user_id, provider, subject, email, email_verified, created_at, last_used_at
		FROM auth_identities WHERE provider = $1 AND subject = $2`, p, subject).
		Scan(&a.ID, &a.UserID, &a.Provider, &a.Subject, &a.Email, &a.EmailVerified, &a.CreatedAt, &a.LastUsedAt)
	if db.IsNoRows(err) {
		return a, apperr.ErrNotFound
	}
	return a, err
}

// InsertIdentity returns a conflict error if (provider, subject) already exists.
func (Repo) InsertIdentity(ctx context.Context, q db.DBTX, a AuthIdentity) error {
	_, err := q.Exec(ctx, `INSERT INTO auth_identities (id, user_id, provider, subject, email, email_verified) VALUES ($1,$2,$3,$4,$5,$6)`,
		a.ID, a.UserID, a.Provider, a.Subject, a.Email, a.EmailVerified)
	if db.IsUniqueViolation(err, "") {
		return apperr.Conflict("identity_exists")
	}
	return err
}

func (Repo) TouchIdentity(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE auth_identities SET last_used_at = now() WHERE id = $1`, id)
	return err
}

func (Repo) InsertAgreement(ctx context.Context, q db.DBTX, userID uuid.UUID, doc, version string) error {
	_, err := q.Exec(ctx, `INSERT INTO user_agreements (user_id, doc, version) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, userID, doc, version)
	return err
}

func (Repo) InsertSession(ctx context.Context, q db.DBTX, s Session, ua string, ip netip.Addr) error {
	var ipp *netip.Addr
	if ip.IsValid() {
		ipp = &ip
	}
	if len(ua) > 300 {
		ua = ua[:300]
	}
	_, err := q.Exec(ctx, `INSERT INTO sessions (id, user_id, token_hash, expires_at, user_agent, ip) VALUES ($1,$2,$3,$4,$5,$6)`,
		s.ID, s.UserID, s.TokenHash, s.ExpiresAt, ua, ipp)
	return err
}

// GetSessionByHash returns only live sessions (not revoked, not expired).
func (Repo) GetSessionByHash(ctx context.Context, q db.DBTX, hash []byte) (Session, error) {
	var s Session
	err := q.QueryRow(ctx, `SELECT id, user_id, token_hash, created_at, last_seen_at, expires_at, revoked_at FROM sessions
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()`, hash).
		Scan(&s.ID, &s.UserID, &s.TokenHash, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.RevokedAt)
	if db.IsNoRows(err) {
		return s, apperr.ErrNotFound
	}
	return s, err
}

func (Repo) TouchSession(ctx context.Context, q db.DBTX, id uuid.UUID, newExpires time.Time) error {
	_, err := q.Exec(ctx, `UPDATE sessions SET last_seen_at = now(), expires_at = $2 WHERE id = $1`, id, newExpires)
	return err
}

func (Repo) RevokeSession(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	return err
}

func (Repo) RevokeAllUserSessions(ctx context.Context, q db.DBTX, userID uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	return err
}

func (Repo) DeleteExpiredSessions(ctx context.Context, q db.DBTX, olderThan time.Time) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM sessions WHERE expires_at < $1 OR revoked_at < $1`, olderThan)
	return tag.RowsAffected(), err
}
