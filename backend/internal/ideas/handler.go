package ideas

import (
	"net/http"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/platform/httpx"
	"beeloy.com/backend/internal/platform/pagination"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	Svc   *Service
	Pool  *pgxpool.Pool
	Users identity.Repo
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/ideas", h.list)
	r.Get("/ideas/{slug}", h.get)
	r.Get("/users/{slug}/ideas", h.byAuthor)
	r.Group(func(r chi.Router) {
		r.Use(identity.RequireAuth, identity.RequireActive)
		r.Post("/ideas", h.create)
		r.Patch("/ideas/{id}", h.edit)
		r.Put("/ideas/{id}/vote", h.vote)
		r.Delete("/ideas/{id}/vote", h.unvote)
		r.Post("/ideas/{id}/status", h.setStatus)
		r.Put("/ideas/{id}/reply", h.reply)
	})
}

func viewer(r *http.Request) *acl.Subject {
	if s, ok := acl.SubjectFrom(r.Context()); ok {
		return &s
	}
	return nil
}

func ideaID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return id, apperr.ErrNotFound
	}
	return id, nil
}

func (h *Handler) parseFilter(r *http.Request) (ListFilter, pagination.Cursor, int, error) {
	q := r.URL.Query()
	var f ListFilter
	limit, err := pagination.ParseLimit(q)
	if err != nil {
		return f, pagination.Cursor{}, 0, err
	}
	cur, err := pagination.Decode(q.Get("cursor"))
	if err != nil {
		return f, cur, 0, err
	}
	if s := q.Get("status"); s != "" {
		st := Status(s)
		if _, ok := transitions[st]; !ok && !st.Terminal() && st != Submitted {
			return f, cur, 0, apperr.ErrBadRequest.WithMessage("bad status")
		}
		f.Status = &st
	}
	if c := q.Get("category"); c != "" {
		if !categories[c] {
			return f, cur, 0, apperr.ErrBadRequest.WithMessage("bad category")
		}
		f.Category = &c
	}
	switch q.Get("sort") {
	case "", "new":
		f.Sort = "new"
	case "top":
		f.Sort = "top"
	default:
		return f, cur, 0, apperr.ErrBadRequest.WithMessage("bad sort")
	}
	return f, cur, limit, nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	f, cur, limit, err := h.parseFilter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := viewer(r)
	if r.URL.Query().Get("mine") == "1" {
		if v == nil {
			httpx.WriteError(w, r, apperr.ErrUnauthorized)
			return
		}
		f.AuthorID = &v.UserID
	}
	items, next, err := h.Svc.List(r.Context(), v, f, cur, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if v == nil {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	httpx.JSONList(w, items, next)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	v := viewer(r)
	idea, hist, canonical, err := h.Svc.GetBySlug(r.Context(), v, chi.URLParam(r, "slug"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if v == nil {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	httpx.JSONWith(w, http.StatusOK, map[string]any{"idea": idea, "history": hist}, map[string]any{"canonical_slug": canonical})
}

// byAuthor serves the profile activity tab. Hidden profiles (profile_public=false) show nothing.
func (h *Handler) byAuthor(w http.ResponseWriter, r *http.Request) {
	u, _, err := h.Users.GetUserBySlug(r.Context(), h.Pool, identity.Normalize(chi.URLParam(r, "slug")))
	if err != nil || u.Status != identity.StatusActive {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	if !u.ProfilePublic {
		httpx.JSONList(w, []Idea{}, "")
		return
	}
	_, cur, limit, err := h.parseFilter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, next, err := h.Svc.List(r.Context(), viewer(r), ListFilter{Sort: "new", AuthorID: &u.ID}, cur, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSONList(w, items, next)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req CreateReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	i, err := h.Svc.Create(r.Context(), identity.MustSubject(r), req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, i)
}

func (h *Handler) edit(w http.ResponseWriter, r *http.Request) {
	id, err := ideaID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req EditReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	i, err := h.Svc.Edit(r.Context(), identity.MustSubject(r), id, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, i)
}

func (h *Handler) vote(w http.ResponseWriter, r *http.Request) {
	id, err := ideaID(r)
	if err == nil {
		err = h.Svc.Vote(r.Context(), identity.MustSubject(r), id)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) unvote(w http.ResponseWriter, r *http.Request) {
	id, err := ideaID(r)
	if err == nil {
		err = h.Svc.Unvote(r.Context(), identity.MustSubject(r), id)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) setStatus(w http.ResponseWriter, r *http.Request) {
	id, err := ideaID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req struct {
		To          Status     `json:"to"`
		Note        string     `json:"note"`
		DuplicateOf *uuid.UUID `json:"duplicate_of_id"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	i, err := h.Svc.SetStatus(r.Context(), identity.MustSubject(r), httpx.ClientIP(r), id, req.To, req.Note, req.DuplicateOf)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, i)
}

func (h *Handler) reply(w http.ResponseWriter, r *http.Request) {
	id, err := ideaID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	i, err := h.Svc.Reply(r.Context(), identity.MustSubject(r), httpx.ClientIP(r), id, req.Text)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, i)
}
