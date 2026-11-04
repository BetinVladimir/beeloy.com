// Package server wires configuration, services and HTTP routes. It is shared by cmd/api and integration tests.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/admin"
	"beeloy.com/backend/internal/articles"
	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/comments"
	"beeloy.com/backend/internal/config"
	"beeloy.com/backend/internal/i18nstore"
	"beeloy.com/backend/internal/ideas"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/jobs"
	"beeloy.com/backend/internal/messenger"
	"beeloy.com/backend/internal/moderation"
	"beeloy.com/backend/internal/platform/httpx"
	pmail "beeloy.com/backend/internal/platform/mail"
	"beeloy.com/backend/internal/platform/maintenance"
	"beeloy.com/backend/internal/platform/metrics"
	"beeloy.com/backend/internal/platform/outbox"
	"beeloy.com/backend/internal/platform/ratelimit"
	"beeloy.com/backend/internal/platform/s3store"
	"beeloy.com/backend/internal/privacy"
	"beeloy.com/backend/internal/registry"
	"beeloy.com/backend/internal/search"
	"beeloy.com/backend/internal/seo"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Deps are the external dependencies of the API process.
type Deps struct {
	Cfg    *config.Config
	Pool   *pgxpool.Pool
	Redis  *redis.Client
	Mailer pmail.Mailer
	Store  s3store.ObjectStore // content storage (articles); nil disables article features
	// Journal records account purges outside PostgreSQL (nil in dev/tests).
	Journal privacy.Journal
	// Social overrides the Google/Facebook adapters (tests use provider stubs); nil builds them from config.
	Social map[identity.Provider]identity.SocialProvider
}

// Server holds the constructed services (exported for tests and background workers).
type Server struct {
	Deps
	Authz      acl.Authorizer
	Audit      audit.Service
	Limiter    *ratelimit.Limiter
	Slugs      *identity.SlugService
	Sessions   *identity.SessionService
	Auth       *identity.AuthService
	OTP        *identity.OTPService
	Identity   *identity.Handler
	Grants     *acl.GrantService
	Relay      *outbox.Relay
	Registry   registry.Repo
	Articles   *articles.Service
	Search     search.Provider
	I18n       *i18nstore.Store
	Ideas      *ideas.Service
	Comments   *comments.Service
	Messenger  *messenger.Service
	Moderation *moderation.Service
	Privacy    *privacy.Service
	Jobs       *jobs.Runner
	Realtime   *outbox.Consumer
	Router     http.Handler
	wg         sync.WaitGroup
	// AfterUserCreate hooks run inside the registration transaction (registry, outbox).
	AfterUserCreate func(ctx context.Context, tx pgx.Tx, u identity.User) error
}

// Build constructs the server (and checks optional dependencies such as S3 once they are used).
func Build(ctx context.Context, d Deps) (*Server, error) { return New(d), nil }

// StartBackground starts relay, consumers and jobs; they stop when ctx is cancelled (see Wait).
func (s *Server) StartBackground(ctx context.Context) {
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.Relay.Run(ctx) }()
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.Realtime.Run(ctx) }()
	s.Jobs.Start(ctx, &s.wg)
}

// MetricsHandler serves /metrics (bind it to an internal address).
func (s *Server) MetricsHandler() http.Handler { return metrics.Handler(s.Pool, s.Redis) }

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "api"
	}
	return h
}

// Wait blocks until background goroutines have finished.
func (s *Server) Wait() { s.wg.Wait() }

func New(d Deps) *Server {
	s := &Server{Deps: d}
	s.Limiter = ratelimit.New(d.Redis)
	s.Slugs = &identity.SlugService{Pool: d.Pool, Audit: s.Audit}
	s.Sessions = &identity.SessionService{Pool: d.Pool, Cfg: identity.DefaultSessionConfig(!d.Cfg.IsDev())}
	s.OTP = &identity.OTPService{RDB: d.Redis, Mailer: d.Mailer, Secret: d.Cfg.OTPHMACSecret, Lim: s.Limiter, Slugs: s.Slugs}
	s.Auth = &identity.AuthService{Pool: d.Pool, Slugs: s.Slugs, Sessions: s.Sessions, Audit: s.Audit,
		TermsVersion: d.Cfg.TermsVersion, PrivacyVersion: d.Cfg.PrivacyVersion}
	s.Identity = &identity.Handler{Pool: d.Pool, OTP: s.OTP, Auth: s.Auth, Sessions: s.Sessions, Slugs: s.Slugs, Lim: s.Limiter, Audit: s.Audit, Dev: d.Cfg.IsDev()}
	social := d.Social
	if social == nil {
		social = map[identity.Provider]identity.SocialProvider{}
		cb := func(p string) string { return d.Cfg.PublicBaseURL + "/api/v1/auth/oauth/" + p + "/callback" }
		devEndpoints := func(p *identity.OAuth2Provider) *identity.OAuth2Provider {
			if d.Cfg.OAuthDevAuthURL != "" {
				p.AuthURL, p.TokenURL, p.UserInfoURL = d.Cfg.OAuthDevAuthURL, d.Cfg.OAuthDevTokenURL, d.Cfg.OAuthDevUserInfoURL
			}
			return p
		}
		if d.Cfg.GoogleClientID != "" {
			social[identity.ProviderGoogle] = devEndpoints(identity.NewGoogle(d.Cfg.GoogleClientID, d.Cfg.GoogleClientSecret, cb("google")))
		}
		if d.Cfg.FacebookClientID != "" {
			social[identity.ProviderFacebook] = devEndpoints(identity.NewFacebook(d.Cfg.FacebookClientID, d.Cfg.FacebookClientSecret, cb("facebook")))
		}
	}
	s.Identity.OAuth = &identity.OAuthService{RDB: d.Redis, Pool: d.Pool, Auth: s.Auth, Providers: social}
	s.Grants = &acl.GrantService{Pool: d.Pool, Audit: s.Audit, Authz: s.Authz}
	s.Relay = outbox.NewRelay(d.Pool, d.Redis)
	hook := func(ctx context.Context, tx pgx.Tx, u identity.User) error {
		return s.Registry.Upsert(ctx, tx, registry.FromUser(u.ID, string(u.Status), u.ProfilePublic))
	}
	s.Auth.AfterUserCreate, s.AfterUserCreate = hook, hook
	s.Search = &search.PGProvider{Pool: d.Pool}
	s.I18n = &i18nstore.Store{Pool: d.Pool, RDB: d.Redis}
	s.Ideas = &ideas.Service{Pool: d.Pool, Slugs: s.Slugs, Authz: s.Authz, Audit: s.Audit, Lim: s.Limiter, Registry: s.Registry, Kick: s.Relay.Kick}
	s.Comments = &comments.Service{Pool: d.Pool, Authz: s.Authz, Audit: s.Audit, Lim: s.Limiter, Registry: s.Registry, Kick: s.Relay.Kick}
	s.Messenger = &messenger.Service{Pool: d.Pool, Authz: s.Authz, RDB: d.Redis, Lim: s.Limiter, Kick: s.Relay.Kick}
	s.Realtime = &outbox.Consumer{RDB: d.Redis, Group: "realtime", Name: hostname(), Handlers: messenger.Realtime(d.Pool, d.Redis)}
	s.Moderation = &moderation.Service{Pool: d.Pool, Authz: s.Authz, Audit: s.Audit, Lim: s.Limiter, Sessions: s.Sessions, Registry: s.Registry, Kick: s.Relay.Kick}
	s.Privacy = &privacy.Service{Pool: d.Pool, Audit: s.Audit, Lim: s.Limiter, Registry: s.Registry, Kick: s.Relay.Kick, Journal: d.Journal}
	s.Jobs = &jobs.Runner{Pool: d.Pool, Privacy: s.Privacy}
	if d.Store != nil {
		s.Articles = &articles.Service{Pool: d.Pool, Store: d.Store, Slugs: s.Slugs, Authz: s.Authz, RDB: d.Redis, Audit: s.Audit, Registry: s.Registry, Kick: s.Relay.Kick}
	}
	s.Router = s.routes()
	return s
}

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.RequestID, metrics.Middleware, httpx.Recoverer, httpx.AccessLog(identity.UserIDForLog),
		httpx.RealIP(httpx.RealIPConfig{TrustedCIDRs: s.Cfg.TrustedProxyCIDRs, SharedSecret: s.Cfg.ProxySharedSecret}),
		httpx.SecurityHeaders, httpx.MaxBody(1<<20))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/readyz", s.readyz)

	r.Route("/api/v1", func(r chi.Router) {
		ri := httpx.RealIPConfig{TrustedCIDRs: s.Cfg.TrustedProxyCIDRs, SharedSecret: s.Cfg.ProxySharedSecret}
		r.Use(httpx.RequireTrustedIngress(ri, s.Cfg.AppEnv == "prod" || s.Cfg.AppEnv == "staging"),
			maintenance.Middleware(s.Redis), identity.LoadSession(s.Sessions), httpx.CSRF(s.Cfg.AllowedOrigins))
		s.Identity.Routes(r)
		(&registry.Handler{Pool: s.Pool}).Routes(r)
		(&i18nstore.Handler{S: s.I18n}).Routes(r)
		(&search.Handler{P: s.Search, Lim: s.Limiter}).Routes(r)
		(&ideas.Handler{Svc: s.Ideas, Pool: s.Pool}).Routes(r)
		(&comments.Handler{Svc: s.Comments}).Routes(r)
		(&messenger.Handler{Svc: s.Messenger, Sessions: s.Sessions}).Routes(r)
		(&seo.Handler{Pool: s.Pool}).Routes(r)
		(&moderation.Handler{Svc: s.Moderation}).Routes(r)
		(&privacy.Handler{Svc: s.Privacy}).Routes(r)
		(&admin.Handler{Pool: s.Pool, Authz: s.Authz, Grants: s.Grants, Audit: s.Audit}).Routes(r)
		if s.Articles != nil {
			(&articles.Handler{Svc: s.Articles}).Routes(r)
		}
	})
	return r
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.Pool.Ping(ctx); err != nil {
		slog.Warn("readyz: postgres", "err", err)
		http.Error(w, `{"status":"postgres unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	if err := s.Redis.Ping(ctx).Err(); err != nil {
		slog.Warn("readyz: redis", "err", err)
		http.Error(w, `{"status":"redis unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ready"}`))
}
