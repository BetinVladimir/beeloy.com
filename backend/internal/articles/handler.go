package articles

import (
	"net/http"
	"strconv"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/platform/httpx"
	"beeloy.com/backend/internal/platform/pagination"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct{ Svc *Service }

// Routes registers article endpoints below /api/v1 (LoadSession and CSRF are applied by the parent router).
func (h *Handler) Routes(r chi.Router) {
	r.Get("/categories", h.categories)
	r.Get("/articles", h.list)
	r.Get("/articles/{slug}", h.get)
	r.Group(func(r chi.Router) {
		r.Use(identity.RequireAuth, identity.RequireActive)
		r.Get("/me/articles", h.myRestricted)
		r.Route("/editor/articles", func(r chi.Router) {
			r.Get("/", h.editorList)
			r.Post("/", h.create)
			r.Get("/{id}", h.editorGet)
			r.Patch("/{id}", h.patch)
			r.Put("/{id}/source", h.putSource)
			r.Put("/{id}/translations/{locale}", h.putTranslation)
			r.Post("/{id}/submit", h.submit)
			r.Get("/{id}/revisions/{rev}/{locale}", h.revisionText)
		})
		r.Route("/mod/articles", func(r chi.Router) {
			r.Get("/", h.queue)
			r.Post("/{id}/publish", h.publish)
			r.Post("/{id}/reject", h.reject)
			r.Post("/{id}/unpublish", h.unpublish)
		})
	})
}

func idParam(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return id, apperr.ErrNotFound
	}
	return id, nil
}

func listParams(r *http.Request) (pagination.Cursor, int, error) {
	limit, err := pagination.ParseLimit(r.URL.Query())
	if err != nil {
		return pagination.Cursor{}, 0, err
	}
	cur, err := pagination.Decode(r.URL.Query().Get("cursor"))
	return cur, limit, err
}

func (h *Handler) categories(w http.ResponseWriter, r *http.Request) {
	cs, err := h.Svc.Categories(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	httpx.JSON(w, http.StatusOK, cs)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	cur, limit, err := listParams(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var cat *string
	if c := r.URL.Query().Get("category"); c != "" {
		cat = &c
	}
	var excl *string
	if c := r.URL.Query().Get("exclude_category"); c != "" {
		excl = &c
	}
	items, next, err := h.Svc.List(r.Context(), r.URL.Query().Get("locale"), cat, excl, cur, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSONList(w, items, next)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	var viewer *acl.Subject
	if s, ok := acl.SubjectFrom(r.Context()); ok {
		viewer = &s
	}
	v, canonical, err := h.Svc.GetBySlug(r.Context(), viewer, chi.URLParam(r, "slug"), r.URL.Query().Get("locale"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if v.Visibility == VisPublic {
		w.Header().Set("Cache-Control", "private, no-store")
	} else {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	httpx.JSONWith(w, http.StatusOK, v, map[string]any{"canonical_slug": canonical})
}

func (h *Handler) myRestricted(w http.ResponseWriter, r *http.Request) {
	cur, limit, err := listParams(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, next, err := h.Svc.ListMyRestricted(r.Context(), identity.MustSubject(r), r.URL.Query().Get("locale"), cur, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSONList(w, items, next)
}

func (h *Handler) editorList(w http.ResponseWriter, r *http.Request) {
	cur, limit, err := listParams(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, next, err := h.Svc.ListForEditor(r.Context(), identity.MustSubject(r), cur, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSONList(w, items, next)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req CreateArticleReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a, err := h.Svc.Create(r.Context(), identity.MustSubject(r), req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, a)
}

func (h *Handler) editorGet(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ea, err := h.Svc.GetForEditor(r.Context(), identity.MustSubject(r), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"article": ea.Article, "source": ea.Source, "revisions": ea.Revisions, "viewer_ids": ea.ViewerIDs})
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req UpdateArticleReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a, err := h.Svc.UpdateMeta(r.Context(), identity.MustSubject(r), id, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a)
}

func (h *Handler) putSource(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req TextReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rev, err := h.Svc.SaveSource(r.Context(), identity.MustSubject(r), id, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"rev": rev})
}

func (h *Handler) putTranslation(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rev := 0
	if v := r.URL.Query().Get("rev"); v != "" {
		if rev, err = strconv.Atoi(v); err != nil {
			httpx.WriteError(w, r, apperr.ErrBadRequest.WithMessage("bad rev"))
			return
		}
	}
	var req TextReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.Svc.PutTranslation(r.Context(), identity.MustSubject(r), id, rev, chi.URLParam(r, "locale"), req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a, err := h.Svc.Submit(r.Context(), identity.MustSubject(r), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a)
}

func (h *Handler) revisionText(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rev, err := strconv.Atoi(chi.URLParam(r, "rev"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	f, err := h.Svc.GetRevisionText(r.Context(), identity.MustSubject(r), id, rev, chi.URLParam(r, "locale"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, f)
}

func (h *Handler) queue(w http.ResponseWriter, r *http.Request) {
	cur, limit, err := listParams(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, next, err := h.Svc.ReviewQueue(r.Context(), identity.MustSubject(r), cur, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSONList(w, items, next)
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req struct {
		Rev        int         `json:"rev"`
		Visibility Visibility  `json:"visibility"`
		ViewerIDs  []uuid.UUID `json:"viewer_ids"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a, err := h.Svc.Publish(r.Context(), identity.MustSubject(r), httpx.ClientIP(r), id, req.Rev, req.Visibility, req.ViewerIDs)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a)
}

func (h *Handler) reject(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a, err := h.Svc.Reject(r.Context(), identity.MustSubject(r), httpx.ClientIP(r), id, req.Note)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a)
}

func (h *Handler) unpublish(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a, err := h.Svc.Unpublish(r.Context(), identity.MustSubject(r), httpx.ClientIP(r), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a)
}
