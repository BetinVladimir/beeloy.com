package identity

import (
	"context"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RegisterOpts are used only when the e-mail has no account yet.
type RegisterOpts struct {
	Nickname    string
	Slug        string
	AcceptTerms bool
}

type AuthService struct {
	Pool           *pgxpool.Pool
	Repo           Repo
	Slugs          *SlugService
	Sessions       *SessionService
	Audit          audit.Service
	TermsVersion   string
	PrivacyVersion string
	// AfterUserChange is called inside the transaction after a user is created (registry/outbox hooks).
	AfterUserCreate func(ctx context.Context, tx pgx.Tx, u User) error
}

func CleanNickname(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	for utf8.RuneCountInString(s) > 100 {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return strings.TrimSpace(s)
}

// LoginOrRegister is the single entry point after a successful OTP verification (the e-mail is proven).
func (a *AuthService) LoginOrRegister(ctx context.Context, email, locale string, opts RegisterOpts, ua string, ip netip.Addr) (User, bool, string, time.Time, error) {
	var user User
	isNew := false
	err := db.WithTx(ctx, a.Pool, func(tx pgx.Tx) error {
		ident, err := a.Repo.FindIdentity(ctx, tx, ProviderEmailOTP, email)
		switch {
		case err == nil:
			if user, err = a.Repo.GetUserByID(ctx, tx, ident.UserID); err != nil {
				return err
			}
			return a.Repo.TouchIdentity(ctx, tx, ident.ID)
		case err != apperr.ErrNotFound:
			return err
		}
		if !opts.AcceptTerms {
			return apperr.Field("accept_terms", "required")
		}
		em := email
		if user, err = a.createAccount(ctx, tx, newAccount{Nickname: opts.Nickname, Slug: opts.Slug, Locale: locale, Provider: ProviderEmailOTP, Subject: email, Email: &em, EmailVerified: true}); err != nil {
			return err
		}
		isNew = true
		if a.AfterUserCreate != nil {
			return a.AfterUserCreate(ctx, tx, user)
		}
		return nil
	})
	if err != nil {
		// a parallel first sign-in with the same e-mail loses on (provider, subject): retry as a plain login
		if apperr.Conflict("identity_exists").Message == errMessage(err) && !isNew {
			return a.LoginOrRegister(ctx, email, locale, opts, ua, ip)
		}
		return User{}, false, "", time.Time{}, err
	}
	switch user.Status {
	case StatusBlocked:
		return User{}, false, "", time.Time{}, apperr.Forbidden("account blocked")
	case StatusDeleted:
		return User{}, false, "", time.Time{}, apperr.ErrInvalidCode
	}
	token, exp, err := a.Sessions.Create(ctx, user.ID, ua, ip)
	if err != nil {
		return User{}, false, "", time.Time{}, err
	}
	_ = a.Audit.Record(ctx, a.Pool, audit.Input{ActorID: &user.ID, Action: audit.ActionLogin, TargetType: "user", TargetID: user.ID.String(), Meta: map[string]any{"is_new": isNew}, IP: ip})
	if user.Slug == "" {
		if u2, e := a.Repo.GetUserByID(ctx, a.Pool, user.ID); e == nil {
			user = u2
		}
	}
	return user, isNew, token, exp, nil
}

func errMessage(err error) string {
	if ae, ok := err.(*apperr.Error); ok {
		return ae.Message
	}
	return ""
}

type newAccount struct {
	Nickname, Slug, Locale string
	Provider               Provider
	Subject                string
	Email                  *string
	EmailVerified          bool
}

// createAccount inserts user, identity, slug and agreements. The public name never derives from the e-mail:
// without an explicit nickname it is "User" and the slug is random.
func (a *AuthService) createAccount(ctx context.Context, tx pgx.Tx, n newAccount) (User, error) {
	nick := CleanNickname(n.Nickname)
	explicit := nick != ""
	if !explicit {
		nick = "User"
	}
	locale := n.Locale
	if !locales.Known(locale) {
		locale = "bg"
	}
	slug := Normalize(n.Slug)
	var err error
	if slug == "" {
		if slug, err = a.Slugs.GenerateForUser(ctx, tx, nick, explicit); err != nil {
			return User{}, err
		}
	}
	user := User{ID: ids.New(), Nickname: nick, Locale: locale, Status: StatusActive, ProfilePublic: true}
	if err := a.Repo.CreateUser(ctx, tx, user); err != nil {
		return User{}, err
	}
	if err := a.Repo.InsertIdentity(ctx, tx, AuthIdentity{ID: ids.New(), UserID: user.ID, Provider: n.Provider, Subject: n.Subject, Email: n.Email, EmailVerified: n.EmailVerified}); err != nil {
		return User{}, err
	}
	if err := a.Slugs.Assign(ctx, tx, EntityUser, user.ID, slug); err != nil {
		return User{}, err
	}
	user.Slug = slug
	if err := a.Repo.InsertAgreement(ctx, tx, user.ID, "terms", a.TermsVersion); err != nil {
		return User{}, err
	}
	if err := a.Repo.InsertAgreement(ctx, tx, user.ID, "privacy", a.PrivacyVersion); err != nil {
		return User{}, err
	}
	return user, nil
}

// StartSession checks the account status and opens a session (used after a verified social sign-in).
func (a *AuthService) StartSession(ctx context.Context, user User, provider, ua string, ip netip.Addr) (string, time.Time, error) {
	if u2, err := a.Repo.GetUserByID(ctx, a.Pool, user.ID); err == nil {
		user = u2
	}
	switch user.Status {
	case StatusBlocked:
		return "", time.Time{}, apperr.Forbidden("account blocked")
	case StatusDeleted:
		return "", time.Time{}, apperr.ErrInvalidCode
	}
	token, exp, err := a.Sessions.Create(ctx, user.ID, ua, ip)
	if err != nil {
		return "", time.Time{}, err
	}
	_ = a.Audit.Record(ctx, a.Pool, audit.Input{ActorID: &user.ID, Action: audit.ActionLogin, TargetType: "user", TargetID: user.ID.String(), Meta: map[string]any{"provider": provider}, IP: ip})
	return token, exp, nil
}
