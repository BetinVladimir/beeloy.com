package identity

import (
	"net/http"
	"net/url"
	"time"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/httpx"
	"beeloy.com/backend/internal/platform/ratelimit"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

var (
	ruleOAuthStart    = ratelimit.Rule{Name: "oauth_start", Limit: 10, Window: time.Minute}
	ruleOAuthCallback = ratelimit.Rule{Name: "oauth_callback", Limit: 30, Window: time.Minute}
)

func (h *Handler) oauthCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: oauthCookiePath, MaxAge: maxAge, HttpOnly: true, Secure: h.Sessions.Cfg.Secure, SameSite: http.SameSiteLaxMode})
}

func providerParam(r *http.Request) Provider { return Provider(chi.URLParam(r, "provider")) }

func (h *Handler) oauthStart(w http.ResponseWriter, r *http.Request) {
	if err := h.Lim.Check(r.Context(), ruleOAuthStart, httpx.ClientIP(r).String(), true); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q := r.URL.Query()
	u, flow, err := h.OAuth.Start(r.Context(), providerParam(r), q.Get("return_to"), q.Get("locale"), false, nil, nil)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.oauthCookie(w, OAuthFlowCookie, flow, int(oauthFlowTTL/time.Second))
	http.Redirect(w, r, u, http.StatusFound)
}

func (h *Handler) oauthCallback(w http.ResponseWriter, r *http.Request) {
	if err := h.Lim.Check(r.Context(), ruleOAuthCallback, httpx.ClientIP(r).String(), true); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q := r.URL.Query()
	flow := ""
	if c, err := r.Cookie(OAuthFlowCookie); err == nil {
		flow = c.Value
	}
	var cur *uuid.UUID
	if s, ok := SessionFrom(r.Context()); ok {
		cur = &s.ID
	}
	res, err := h.OAuth.Callback(r.Context(), providerParam(r), q.Get("code"), q.Get("state"), flow, cur, r.UserAgent(), httpx.ClientIP(r))
	h.oauthCookie(w, OAuthFlowCookie, "", -1)
	loc := res.Locale
	if loc == "" {
		loc = "bg"
	}
	switch {
	case err != nil:
		if ae, ok := err.(*apperr.Error); ok && ae.Code == "unavailable" {
			httpx.WriteError(w, r, err)
			return
		}
		to := "/" + loc + "/login?error=oauth"
		if res.Linked || cur != nil {
			to = "/" + loc + "/settings?error=oauth"
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
	case res.OnboardingToken != "":
		h.oauthCookie(w, OnboardingCookie, res.OnboardingToken, int(oauthFlowTTL/time.Second))
		http.Redirect(w, r, "/"+loc+"/login?step=onboarding&return_to="+url.QueryEscape(res.ReturnTo), http.StatusSeeOther)
	default:
		if res.Session != "" {
			SetCookie(w, h.Sessions.Cfg, res.Session, res.SessionExp)
		}
		http.Redirect(w, r, res.ReturnTo, http.StatusSeeOther)
	}
}

func (h *Handler) oauthComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nickname    string `json:"nickname"`
		Slug        string `json:"slug"`
		Locale      string `json:"locale"`
		AcceptTerms bool   `json:"accept_terms"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	tok := ""
	if c, err := r.Cookie(OnboardingCookie); err == nil {
		tok = c.Value
	}
	u, token, exp, err := h.OAuth.Complete(r.Context(), tok, req.Nickname, req.Slug, req.Locale, req.AcceptTerms, r.UserAgent(), httpx.ClientIP(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.oauthCookie(w, OnboardingCookie, "", -1)
	bs, err := h.Sessions.ACL.ListBindings(r.Context(), h.Pool, u.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	SetCookie(w, h.Sessions.Cfg, token, exp)
	httpx.JSON(w, http.StatusOK, map[string]any{"user": BuildMe(u, bs), "is_new": true})
}

func (h *Handler) listIdentities(w http.ResponseWriter, r *http.Request) {
	out, err := h.OAuth.List(r.Context(), MustSubject(r).UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) linkIdentity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReturnTo string `json:"return_to"`
	}
	_ = httpx.DecodeJSON(r, &req)
	subj := MustSubject(r)
	sess, _ := SessionFrom(r.Context())
	me, err := h.Repo.GetUserByID(r.Context(), h.Pool, subj.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	u, flow, err := h.OAuth.Start(r.Context(), providerParam(r), req.ReturnTo, me.Locale, true, &subj.UserID, &sess.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.oauthCookie(w, OAuthFlowCookie, flow, int(oauthFlowTTL/time.Second))
	httpx.JSON(w, http.StatusOK, map[string]string{"authorization_url": u})
}

func (h *Handler) unlinkIdentity(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	if err := h.OAuth.Unlink(r.Context(), MustSubject(r).UserID, id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
