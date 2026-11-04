package messenger

import (
	"net/http"
	"strconv"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/platform/httpx"
	"beeloy.com/backend/internal/platform/pagination"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	Svc      *Service
	Sessions *identity.SessionService
	streams  streamCounter
}

func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(identity.RequireAuth, identity.RequireActive)
		r.Get("/conversations", h.list)
		r.Get("/conversations/unread-count", h.counters)
		r.Post("/conversations", h.start)
		r.Get("/conversations/{id}/messages", h.history)
		r.Post("/conversations/{id}/messages", h.send)
		r.Post("/conversations/{id}/read", h.read)
		r.Post("/conversations/{id}/accept", h.respond(true))
		r.Post("/conversations/{id}/decline", h.respond(false))
		r.Put("/conversations/{id}/mute", h.mute(true))
		r.Delete("/conversations/{id}/mute", h.mute(false))
		r.Put("/users/{id}/block", h.block(true))
		r.Delete("/users/{id}/block", h.block(false))
		r.Get("/events", h.events)
	})
}

func uuidParam(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return id, apperr.ErrNotFound
	}
	return id, nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := pagination.ParseLimit(r.URL.Query())
	var cur pagination.Cursor
	if err == nil {
		cur, err = pagination.Decode(r.URL.Query().Get("cursor"))
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, next, err := h.Svc.Conversations(r.Context(), identity.MustSubject(r), r.URL.Query().Get("folder"), cur, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSONList(w, items, next)
}

func (h *Handler) counters(w http.ResponseWriter, r *http.Request) {
	total, reqs, err := h.Svc.Counters(r.Context(), identity.MustSubject(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int64{"total": total, "requests": reqs})
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID uuid.UUID `json:"user_id"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, created, err := h.Svc.StartConversation(r.Context(), identity.MustSubject(r), req.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, c)
}

func optInt(r *http.Request, name string) (*int64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return nil, apperr.ErrBadRequest.WithMessage("bad " + name)
	}
	return &n, nil
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	var before, after *int64
	if err == nil {
		before, err = optInt(r, "before_seq")
	}
	if err == nil {
		after, err = optInt(r, "after_seq")
	}
	limit := 30
	if err == nil {
		limit, err = pagination.ParseLimitMax(r.URL.Query(), 30, 100)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	msgs, err := h.Svc.History(r.Context(), identity.MustSubject(r), id, before, after, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSONList(w, msgs, "")
}

func (h *Handler) send(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req struct {
		ClientID uuid.UUID `json:"client_id"`
		Body     string    `json:"body"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.ClientID == uuid.Nil {
		httpx.WriteError(w, r, apperr.Field("client_id", "required"))
		return
	}
	m, created, err := h.Svc.Send(r.Context(), identity.MustSubject(r), id, req.ClientID, req.Body)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, m)
}

func (h *Handler) read(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req struct {
		Seq int64 `json:"seq"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.Svc.MarkRead(r.Context(), identity.MustSubject(r), id, req.Seq); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) respond(accept bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuidParam(r, "id")
		if err == nil {
			err = h.Svc.Respond(r.Context(), identity.MustSubject(r), id, accept)
		}
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.NoContent(w)
	}
}

func (h *Handler) mute(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuidParam(r, "id")
		if err == nil {
			err = h.Svc.SetMuted(r.Context(), identity.MustSubject(r), id, on)
		}
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.NoContent(w)
	}
}

func (h *Handler) block(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuidParam(r, "id")
		if err == nil {
			if on {
				err = h.Svc.BlockUser(r.Context(), identity.MustSubject(r), id)
			} else {
				err = h.Svc.UnblockUser(r.Context(), identity.MustSubject(r), id)
			}
		}
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.NoContent(w)
	}
}
