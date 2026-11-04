package identity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/crypto"
	"beeloy.com/backend/internal/platform/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	ProviderGoogle   Provider = "google"
	ProviderFacebook Provider = "facebook"

	oauthFlowTTL = 10 * time.Minute
	// Cookie names are part of the contract (ТЗ-D16, 03 §8).
	OAuthFlowCookie       = "bl_oauth_flow"
	OnboardingCookie      = "bl_onboarding"
	oauthCookiePath       = "/api/v1/auth/oauth"
	maxProviderBody       = 1 << 20
	errLastIdentityReason = "last_identity"
)

// ProviderIdentity is what a provider proved about the signed-in person. Only Subject is trusted as an identifier.
type ProviderIdentity struct {
	Provider          Provider
	Subject           string
	Email             string // optional, only when the provider reports it as verified
	SuggestedNickname string // never published without an explicit choice
}

// SocialProvider hides the provider specifics from the login/link/complete/unlink services.
type SocialProvider interface {
	AuthorizationURL(state, challenge, nonce string) string
	ExchangeAndVerify(ctx context.Context, code, verifier, nonce string) (ProviderIdentity, error)
}

// OAuth2Provider is a server-side authorization-code client. The provider user ID is confirmed by a server-to-server
// request with the access token, so nothing the browser sends is treated as identity.
type OAuth2Provider struct {
	Name         Provider
	ClientID     string
	ClientSecret string
	RedirectURL  string
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
	Scope        string
	UsePKCE      bool
	HTTP         *http.Client
	parse        func(raw []byte) (ProviderIdentity, error)
}

func (p *OAuth2Provider) AuthorizationURL(state, challenge, _ string) string {
	q := url.Values{"client_id": {p.ClientID}, "redirect_uri": {p.RedirectURL}, "response_type": {"code"}, "scope": {p.Scope}, "state": {state}}
	if p.UsePKCE {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	return p.AuthURL + "?" + q.Encode()
}

func (p *OAuth2Provider) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (p *OAuth2Provider) ExchangeAndVerify(ctx context.Context, code, verifier, _ string) (ProviderIdentity, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {p.RedirectURL}, "client_id": {p.ClientID}, "client_secret": {p.ClientSecret}}
	if p.UsePKCE {
		form.Set("code_verifier", verifier)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	raw, err := p.do(req)
	if err != nil {
		return ProviderIdentity{}, err
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(raw, &tok) != nil || tok.AccessToken == "" {
		return ProviderIdentity{}, errors.New("no access token")
	}
	ureq, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.UserInfoURL, nil)
	ureq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	ureq.Header.Set("Accept", "application/json")
	raw, err = p.do(ureq)
	if err != nil {
		return ProviderIdentity{}, err
	}
	pi, err := p.parse(raw)
	if err != nil || pi.Subject == "" {
		return ProviderIdentity{}, errors.New("no provider subject")
	}
	pi.Provider = p.Name
	return pi, nil
}

func (p *OAuth2Provider) do(req *http.Request) ([]byte, error) {
	res, err := p.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxProviderBody))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("provider status " + res.Status) // body is never surfaced
	}
	return raw, nil
}

// NewGoogle / NewFacebook build the production adapters. Endpoints follow the providers' public documentation
// and must be re-checked against it before launch (ТЗ 03 §8).
func NewGoogle(clientID, secret, redirect string) *OAuth2Provider {
	return &OAuth2Provider{Name: ProviderGoogle, ClientID: clientID, ClientSecret: secret, RedirectURL: redirect, UsePKCE: true,
		AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token",
		UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo", Scope: "openid email profile", parse: parseGoogle}
}

func NewFacebook(clientID, secret, redirect string) *OAuth2Provider {
	return &OAuth2Provider{Name: ProviderFacebook, ClientID: clientID, ClientSecret: secret, RedirectURL: redirect,
		AuthURL: "https://www.facebook.com/v19.0/dialog/oauth", TokenURL: "https://graph.facebook.com/v19.0/oauth/access_token",
		UserInfoURL: "https://graph.facebook.com/me?fields=id,name,email", Scope: "public_profile,email", parse: parseFacebook}
}

func parseGoogle(raw []byte) (ProviderIdentity, error) {
	var v struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ProviderIdentity{}, err
	}
	pi := ProviderIdentity{Subject: v.Sub, SuggestedNickname: v.Name}
	if v.EmailVerified {
		pi.Email = strings.ToLower(strings.TrimSpace(v.Email))
	}
	return pi, nil
}

func parseFacebook(raw []byte) (ProviderIdentity, error) {
	var v struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ProviderIdentity{}, err
	}
	// Facebook does not state that the address is verified: it is not used as a contact.
	return ProviderIdentity{Subject: v.ID, SuggestedNickname: v.Name}, nil
}

type flowState struct {
	Provider  Provider   `json:"p"`
	Verifier  string     `json:"v"`
	FlowHash  string     `json:"f"`
	Locale    string     `json:"l"`
	ReturnTo  string     `json:"r"`
	Link      bool       `json:"k"`
	UserID    *uuid.UUID `json:"u,omitempty"`
	SessionID *uuid.UUID `json:"s,omitempty"`
}

type onboarding struct {
	Provider Provider `json:"p"`
	Subject  string   `json:"s"`
	Email    string   `json:"e"`
	Locale   string   `json:"l"`
	ReturnTo string   `json:"r"`
}

// OAuthService runs login, link, onboarding and unlink for social providers.
type OAuthService struct {
	RDB       *redis.Client
	Pool      *pgxpool.Pool
	Repo      Repo
	Auth      *AuthService
	Providers map[Provider]SocialProvider
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func sanitizeReturnTo(s, locale string) string {
	def := "/" + locale
	if s == "" || len(s) > 200 || s[0] != '/' || strings.HasPrefix(s, "//") || strings.ContainsAny(s, "\\\r\n\t") {
		return def
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return def
		}
	}
	return s
}

func (s *OAuthService) provider(p Provider) (SocialProvider, error) {
	sp, ok := s.Providers[p]
	if !ok || sp == nil {
		return nil, apperr.ErrNotFound
	}
	return sp, nil
}

// Start creates the flow state and returns the provider URL plus the browser-bound flow token (cookie value).
func (s *OAuthService) Start(ctx context.Context, p Provider, returnTo, locale string, link bool, userID, sessionID *uuid.UUID) (authURL, flowToken string, err error) {
	sp, err := s.provider(p)
	if err != nil {
		return "", "", err
	}
	if !locales.Known(locale) {
		locale = "bg"
	}
	state, err := crypto.RandomToken(32)
	if err != nil {
		return "", "", err
	}
	flowToken, err = crypto.RandomToken(32)
	if err != nil {
		return "", "", err
	}
	verifier, err := crypto.RandomToken(48)
	if err != nil {
		return "", "", err
	}
	nonce, err := crypto.RandomToken(16)
	if err != nil {
		return "", "", err
	}
	st := flowState{Provider: p, Verifier: verifier, FlowHash: sha(flowToken), Locale: locale, ReturnTo: sanitizeReturnTo(returnTo, locale), Link: link, UserID: userID, SessionID: sessionID}
	raw, _ := json.Marshal(st)
	if err := s.RDB.Set(ctx, "oauth:state:"+sha(state), raw, oauthFlowTTL).Err(); err != nil {
		return "", "", apperr.ErrUnavailable.Wrap(err)
	}
	ch := sha256.Sum256([]byte(verifier))
	return sp.AuthorizationURL(state, base64.RawURLEncoding.EncodeToString(ch[:]), nonce), flowToken, nil
}

// CallbackResult tells the handler where to send the browser.
type CallbackResult struct {
	Locale, ReturnTo string
	Session          string // session token for a login
	SessionExp       time.Time
	OnboardingToken  string
	Linked           bool
}

var errOAuth = apperr.ErrBadRequest.WithMessage("oauth_failed")

// Callback consumes the state exactly once and finishes the flow. Every failure is the same generic error.
func (s *OAuthService) Callback(ctx context.Context, p Provider, code, state, flowToken string, currentSession *uuid.UUID, ua string, ip netip.Addr) (CallbackResult, error) {
	sp, err := s.provider(p)
	if err != nil {
		return CallbackResult{}, err
	}
	if code == "" || state == "" || flowToken == "" {
		return CallbackResult{}, errOAuth
	}
	raw, err := s.RDB.GetDel(ctx, "oauth:state:"+sha(state)).Bytes() // atomic one-time consumption
	if err == redis.Nil {
		return CallbackResult{}, errOAuth
	}
	if err != nil {
		return CallbackResult{}, apperr.ErrUnavailable.Wrap(err)
	}
	var st flowState
	if json.Unmarshal(raw, &st) != nil || st.Provider != p || subtle.ConstantTimeCompare([]byte(st.FlowHash), []byte(sha(flowToken))) != 1 {
		return CallbackResult{}, errOAuth
	}
	res := CallbackResult{Locale: st.Locale, ReturnTo: st.ReturnTo}
	if st.Link && (currentSession == nil || st.SessionID == nil || *currentSession != *st.SessionID) {
		return res, errOAuth // the link must finish in the session that started it
	}
	pi, err := sp.ExchangeAndVerify(ctx, code, st.Verifier, "")
	if err != nil || pi.Subject == "" {
		return res, errOAuth
	}
	if st.Link {
		return res, s.link(ctx, *st.UserID, pi, &res)
	}
	ident, err := s.Repo.FindIdentity(ctx, s.Pool, p, pi.Subject)
	switch {
	case err == nil:
		u, e := s.Repo.GetUserByID(ctx, s.Pool, ident.UserID)
		if e != nil {
			return res, e
		}
		_ = s.Repo.TouchIdentity(ctx, s.Pool, ident.ID)
		res.Session, res.SessionExp, err = s.Auth.StartSession(ctx, u, string(p), ua, ip)
		return res, err
	case !errors.Is(err, apperr.ErrNotFound):
		return res, err
	}
	tok, err := crypto.RandomToken(32)
	if err != nil {
		return res, err
	}
	ob, _ := json.Marshal(onboarding{Provider: p, Subject: pi.Subject, Email: pi.Email, Locale: st.Locale, ReturnTo: st.ReturnTo})
	if err := s.RDB.Set(ctx, "oauth:onb:"+sha(tok), ob, oauthFlowTTL).Err(); err != nil {
		return res, apperr.ErrUnavailable.Wrap(err)
	}
	res.OnboardingToken = tok
	return res, nil
}

func (s *OAuthService) link(ctx context.Context, userID uuid.UUID, pi ProviderIdentity, res *CallbackResult) error {
	err := db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
			return err
		}
		ex, err := s.Repo.FindIdentity(ctx, tx, pi.Provider, pi.Subject)
		switch {
		case err == nil && ex.UserID == userID:
			return nil
		case err == nil:
			return apperr.Conflict("identity_taken") // never re-linked from another account
		case !errors.Is(err, apperr.ErrNotFound):
			return err
		}
		var email *string
		if pi.Email != "" {
			email = &pi.Email
		}
		if err := s.Repo.InsertIdentity(ctx, tx, AuthIdentity{ID: uuid.Must(uuid.NewV7()), UserID: userID, Provider: pi.Provider, Subject: pi.Subject, Email: email, EmailVerified: email != nil}); err != nil {
			return err
		}
		return s.Auth.Audit.Record(ctx, tx, audit.Input{ActorID: &userID, Action: "auth.identity_link", TargetType: "user", TargetID: userID.String(), Meta: map[string]any{"provider": pi.Provider}})
	})
	if err == nil {
		res.Linked = true
	}
	return err
}

// Complete creates the account for a new social identity. The onboarding token is consumed atomically.
func (s *OAuthService) Complete(ctx context.Context, token, nickname, slug, locale string, acceptTerms bool, ua string, ip netip.Addr) (User, string, time.Time, error) {
	if token == "" {
		return User{}, "", time.Time{}, apperr.ErrUnauthorized
	}
	if !acceptTerms {
		return User{}, "", time.Time{}, apperr.Field("accept_terms", "required")
	}
	if slug != "" {
		if err := Validate(EntityUser, Normalize(slug)); err != nil {
			return User{}, "", time.Time{}, err
		}
		if ok, err := s.Auth.Slugs.IsAvailable(ctx, s.Pool, EntityUser, slug); err != nil {
			return User{}, "", time.Time{}, err
		} else if !ok {
			return User{}, "", time.Time{}, apperr.Conflict("slug_taken")
		}
	}
	raw, err := s.RDB.GetDel(ctx, "oauth:onb:"+sha(token)).Bytes()
	if err == redis.Nil {
		return User{}, "", time.Time{}, apperr.ErrUnauthorized
	}
	if err != nil {
		return User{}, "", time.Time{}, apperr.ErrUnavailable.Wrap(err)
	}
	var ob onboarding
	if json.Unmarshal(raw, &ob) != nil {
		return User{}, "", time.Time{}, apperr.ErrUnauthorized
	}
	if locale == "" {
		locale = ob.Locale
	}
	var user User
	err = db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var email *string
		if ob.Email != "" {
			email = &ob.Email
		}
		u, err := s.Auth.createAccount(ctx, tx, newAccount{Nickname: nickname, Slug: slug, Locale: locale, Provider: ob.Provider, Subject: ob.Subject, Email: email, EmailVerified: email != nil})
		if err != nil {
			return err
		}
		user = u
		if s.Auth.AfterUserCreate != nil {
			return s.Auth.AfterUserCreate(ctx, tx, u)
		}
		return nil
	})
	if err != nil {
		return User{}, "", time.Time{}, err
	}
	tok, exp, err := s.Auth.StartSession(ctx, user, string(ob.Provider), ua, ip)
	return user, tok, exp, err
}

// IdentityDTO is the public-to-owner view: no subject, e-mail or tokens.
type IdentityDTO struct {
	ID        uuid.UUID `json:"id"`
	Provider  Provider  `json:"provider"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *OAuthService) List(ctx context.Context, userID uuid.UUID) ([]IdentityDTO, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, provider, created_at FROM auth_identities WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IdentityDTO{}
	for rows.Next() {
		var d IdentityDTO
		if err := rows.Scan(&d.ID, &d.Provider, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Unlink removes the owner's identity unless it is the last sign-in method (409 last_identity).
func (s *OAuthService) Unlink(ctx context.Context, userID, identityID uuid.UUID) error {
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM auth_identities WHERE user_id = $1`, userID).Scan(&n); err != nil {
			return err
		}
		var owned bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_identities WHERE id = $1 AND user_id = $2)`, identityID, userID).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return apperr.ErrNotFound
		}
		if n <= 1 {
			return apperr.Conflict(errLastIdentityReason)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM auth_identities WHERE id = $1 AND user_id = $2`, identityID, userID); err != nil {
			return err
		}
		return s.Auth.Audit.Record(ctx, tx, audit.Input{ActorID: &userID, Action: "auth.identity_unlink", TargetType: "user", TargetID: userID.String()})
	})
}
