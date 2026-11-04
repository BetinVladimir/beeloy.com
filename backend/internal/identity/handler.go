package identity

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/httpx"
	"beeloy.com/backend/internal/platform/ratelimit"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RoleDTO struct {
	Role      acl.Role      `json:"role"`
	ScopeType acl.ScopeType `json:"scope_type"`
	ScopeID   *uuid.UUID    `json:"scope_id"`
}

// Me is the private view of the signed-in user.
type Me struct {
	ID            uuid.UUID `json:"id"`
	Nickname      string    `json:"nickname"`
	Bio           string    `json:"bio"`
	AvatarURL     *string   `json:"avatar_url"`
	Slug          string    `json:"slug"`
	Locale        string    `json:"locale"`
	Status        string    `json:"status"`
	ProfilePublic bool      `json:"profile_public"`
	Roles         []RoleDTO `json:"roles"`
	CreatedAt     time.Time `json:"created_at"`
}

func BuildMe(u User, bindings []acl.Binding) Me {
	roles := make([]RoleDTO, 0, len(bindings))
	for _, b := range bindings {
		roles = append(roles, RoleDTO{Role: b.Role, ScopeType: b.ScopeType, ScopeID: b.ScopeID})
	}
	return Me{ID: u.ID, Nickname: u.Nickname, Bio: u.Bio, AvatarURL: u.AvatarURL, Slug: u.Slug, Locale: u.Locale, Status: string(u.Status),
		ProfilePublic: u.ProfilePublic, Roles: roles, CreatedAt: u.CreatedAt}
}

type Handler struct {
	Pool     *pgxpool.Pool
	Repo     Repo
	OTP      *OTPService
	Auth     *AuthService
	Sessions *SessionService
	Slugs    *SlugService
	Lim      *ratelimit.Limiter
	Audit    audit.Service
	OAuth    *OAuthService
	Dev      bool // registers POST /dev/login (APP_ENV=dev only)
}

// Routes registers the identity endpoints below /api/v1. Public ones need no session; the rest are wrapped here.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/auth/otp/request", h.otpRequest)
	r.Post("/auth/otp/verify", h.otpVerify)
	r.Get("/users/{slug}", h.publicProfile)
	r.Get("/slugs/check", h.slugCheck)
	if h.OAuth != nil {
		r.Get("/auth/oauth/{provider}/start", h.oauthStart)
		r.Get("/auth/oauth/{provider}/callback", h.oauthCallback)
		r.Post("/auth/oauth/complete", h.oauthComplete)
	}
	if h.Dev {
		r.Post("/dev/login", h.devLogin)
	}
	r.Group(func(r chi.Router) {
		r.Use(RequireAuth)
		r.Post("/auth/logout", h.logout)
		r.Get("/me", h.getMe)
		r.Group(func(r chi.Router) {
			r.Use(RequireActive)
			r.Patch("/me", h.patchMe)
			r.Put("/me/slug", h.putSlug)
			if h.OAuth != nil {
				r.Get("/me/identities", h.listIdentities)
				r.Post("/me/identities/{provider}/link", h.linkIdentity)
				r.Delete("/me/identities/{id}", h.unlinkIdentity)
			}
		})
	})
}

func (h *Handler) otpRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email  string `json:"email"`
		Locale string `json:"locale"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	retry, err := h.OTP.Request(r.Context(), req.Email, req.Locale, httpx.ClientIP(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]int{"retry_after": retry})
}

func (h *Handler) otpVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string `json:"email"`
		Code        string `json:"code"`
		Locale      string `json:"locale"`
		AcceptTerms bool   `json:"accept_terms"`
		Nickname    string `json:"nickname"`
		Slug        string `json:"slug"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// the requested slug only matters for a new account; an existing one ignores nickname/slug
	existing := false
	if em, err := NormalizeEmail(req.Email); err == nil {
		_, ferr := h.Repo.FindIdentity(r.Context(), h.Pool, ProviderEmailOTP, em)
		existing = ferr == nil
	}
	if req.Slug != "" && !existing {
		if err := h.OTP.Precheck(r.Context(), req.Slug); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	email, err := h.OTP.Verify(r.Context(), req.Email, req.Code, httpx.ClientIP(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.finishLogin(w, r, email, req.Locale, RegisterOpts{Nickname: req.Nickname, Slug: req.Slug, AcceptTerms: req.AcceptTerms})
}

func (h *Handler) finishLogin(w http.ResponseWriter, r *http.Request, email, locale string, opts RegisterOpts) {
	u, isNew, token, exp, err := h.Auth.LoginOrRegister(r.Context(), email, locale, opts, r.UserAgent(), httpx.ClientIP(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	bs, err := h.Sessions.ACL.ListBindings(r.Context(), h.Pool, u.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	SetCookie(w, h.Sessions.Cfg, token, exp)
	httpx.JSON(w, http.StatusOK, map[string]any{"user": BuildMe(u, bs), "is_new": isNew})
}

// devLogin signs in without a code. Registered only when APP_ENV=dev.
func (h *Handler) devLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	email, err := NormalizeEmail(req.Email)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.finishLogin(w, r, email, "bg", RegisterOpts{AcceptTerms: true})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if sess, ok := SessionFrom(r.Context()); ok {
		_ = h.Sessions.Revoke(r.Context(), sess.ID)
		uid := sess.UserID
		_ = h.Audit.Record(r.Context(), h.Pool, audit.Input{ActorID: &uid, Action: audit.ActionLogout, TargetType: "user", TargetID: uid.String(), IP: httpx.ClientIP(r)})
	}
	ClearCookie(w, h.Sessions.Cfg)
	httpx.NoContent(w)
}

func (h *Handler) loadMe(r *http.Request) (Me, error) {
	subj := MustSubject(r)
	u, err := h.Repo.GetUserByID(r.Context(), h.Pool, subj.UserID)
	if err != nil {
		return Me{}, err
	}
	return BuildMe(u, subj.Bindings), nil
}

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request) {
	me, err := h.loadMe(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, me)
}

func (h *Handler) patchMe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nickname      *string `json:"nickname"`
		Bio           *string `json:"bio"`
		Locale        *string `json:"locale"`
		ProfilePublic *bool   `json:"profile_public"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Nickname != nil {
		n := CleanNickname(*req.Nickname)
		if n == "" {
			fields["nickname"] = "required"
		}
		req.Nickname = &n
	}
	if req.Bio != nil && utf8.RuneCountInString(*req.Bio) > 500 {
		fields["bio"] = "too_long"
	}
	if req.Locale != nil && !locales.Known(*req.Locale) {
		fields["locale"] = "invalid_value"
	}
	if len(fields) > 0 {
		httpx.WriteError(w, r, apperr.Validation(fields))
		return
	}
	if req.Bio != nil {
		b := strings.TrimSpace(*req.Bio)
		req.Bio = &b
	}
	subj := MustSubject(r)
	if err := h.Repo.UpdateProfile(r.Context(), h.Pool, subj.UserID, req.Nickname, req.Bio, req.Locale, req.ProfilePublic); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.getMe(w, r)
}

func (h *Handler) putSlug(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug string `json:"slug"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.Slugs.ChangeUserSlug(r.Context(), MustSubject(r).UserID, httpx.ClientIP(r), req.Slug); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.getMe(w, r)
}

var ruleSlugCheck = ratelimit.Rule{Name: "slug_check", Limit: 30, Window: time.Minute}

func (h *Handler) slugCheck(w http.ResponseWriter, r *http.Request) {
	if err := h.Lim.Check(r.Context(), ruleSlugCheck, httpx.ClientIP(r).String(), true); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	t := EntityType(r.URL.Query().Get("type"))
	if t != EntityUser && t != EntityIdea && t != EntityArticle {
		httpx.WriteError(w, r, apperr.ErrBadRequest.WithMessage("bad type"))
		return
	}
	ok, err := h.Slugs.IsAvailable(r.Context(), h.Pool, t, r.URL.Query().Get("slug"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"available": ok})
}

func (h *Handler) publicProfile(w http.ResponseWriter, r *http.Request) {
	slug := Normalize(chi.URLParam(r, "slug"))
	u, canonical, err := h.Repo.GetUserBySlug(r.Context(), h.Pool, slug)
	if err != nil || u.Status != StatusActive {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	bio := u.Bio
	if !u.ProfilePublic {
		bio = ""
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id": u.ID, "nickname": u.Nickname, "slug": slug, "canonical_slug": canonical, "avatar_url": u.AvatarURL,
		"bio": bio, "created_at": u.CreatedAt, "activity_visible": u.ProfilePublic,
	})
}
