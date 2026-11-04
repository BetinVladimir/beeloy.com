// Package identity covers users, e-mail OTP sign-in, sessions and public slugs.
package identity

import (
	"time"

	"github.com/google/uuid"
)

type UserStatus string

const (
	StatusActive          UserStatus = "active"
	StatusBlocked         UserStatus = "blocked"
	StatusPendingDeletion UserStatus = "pending_deletion"
	StatusDeleted         UserStatus = "deleted"
)

type User struct {
	ID            uuid.UUID
	Nickname      string
	Bio           string
	AvatarURL     *string
	Locale        string
	Status        UserStatus
	ProfilePublic bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Slug          string // current slug (joined from slugs)
}

type Provider string

const ProviderEmailOTP Provider = "email_otp" // the only sign-in method

type AuthIdentity struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	Provider      Provider
	Subject       string
	Email         *string
	EmailVerified bool
	CreatedAt     time.Time
	LastUsedAt    *time.Time
}

type Session struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	TokenHash  []byte
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}
