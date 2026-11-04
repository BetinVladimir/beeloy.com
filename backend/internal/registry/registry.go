// Package registry keeps the common index of public resources (user, article, idea, comment).
// Entity tables stay the source of truth; services update the registry in the same transaction.
package registry

import (
	"context"
	"errors"
	"net/http"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Type string

const (
	TypeUser    Type = "user"
	TypeArticle Type = "article"
	TypeIdea    Type = "idea"
	TypeComment Type = "comment"
)

type Status string

const (
	StatusPending Status = "pending"
	StatusActive  Status = "active"
	StatusHidden  Status = "hidden"
	StatusRemoved Status = "removed"
)

const (
	VisPublic     = "public"
	VisRestricted = "restricted"
	VisPrivate    = "private"
)

type Resource struct {
	ID         uuid.UUID
	Type       Type
	OwnerID    *uuid.UUID
	Status     Status
	Visibility string
	ParentID   *uuid.UUID
}

func (r Resource) PubliclyVisible() bool {
	return r.Status == StatusActive && r.Visibility == VisPublic
}

// FromUser maps users.status / profile_public.
func FromUser(id uuid.UUID, status string, profilePublic bool) Resource {
	st := StatusHidden
	switch status {
	case "active":
		st = StatusActive
	case "deleted":
		st = StatusRemoved
	}
	vis := VisPublic
	if !profilePublic {
		vis = VisRestricted
	}
	return Resource{ID: id, Type: TypeUser, OwnerID: &id, Status: st, Visibility: vis}
}

// FromArticle maps articles.status / visibility: unpublished articles are private.
func FromArticle(id uuid.UUID, author *uuid.UUID, status, visibility string) Resource {
	r := Resource{ID: id, Type: TypeArticle, OwnerID: author, Visibility: VisPrivate}
	switch status {
	case "published":
		r.Status, r.Visibility = StatusActive, visibility
	case "archived":
		r.Status = StatusHidden
	default:
		r.Status = StatusPending
	}
	return r
}

func FromIdea(id uuid.UUID, author *uuid.UUID) Resource {
	return Resource{ID: id, Type: TypeIdea, OwnerID: author, Status: StatusActive, Visibility: VisPublic}
}

// FromComment maps comments.status; parent is the article or idea id.
func FromComment(id uuid.UUID, author *uuid.UUID, parent uuid.UUID, status string) Resource {
	st := StatusActive
	switch status {
	case "hidden":
		st = StatusHidden
	case "deleted":
		st = StatusRemoved
	}
	return Resource{ID: id, Type: TypeComment, OwnerID: author, Status: st, Visibility: VisPublic, ParentID: &parent}
}

type Repo struct{}

func (Repo) Upsert(ctx context.Context, q db.DBTX, r Resource) error {
	_, err := q.Exec(ctx, `INSERT INTO resources (id, type, owner_id, status, visibility, parent_id) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (id) DO UPDATE SET owner_id = EXCLUDED.owner_id, status = EXCLUDED.status, visibility = EXCLUDED.visibility,
		parent_id = EXCLUDED.parent_id, updated_at = now()`, r.ID, r.Type, r.OwnerID, r.Status, r.Visibility, r.ParentID)
	return err
}

func (Repo) Get(ctx context.Context, q db.DBTX, id uuid.UUID) (Resource, error) {
	var r Resource
	err := q.QueryRow(ctx, `SELECT id, type, owner_id, status, visibility, parent_id FROM resources WHERE id = $1`, id).
		Scan(&r.ID, &r.Type, &r.OwnerID, &r.Status, &r.Visibility, &r.ParentID)
	if db.IsNoRows(err) {
		return r, apperr.ErrNotFound
	}
	return r, err
}

// CanonicalPath returns the locale-less public path of a resource.
func (rp Repo) CanonicalPath(ctx context.Context, q db.DBTX, id uuid.UUID) (string, error) {
	r, err := rp.Get(ctx, q, id)
	if err != nil {
		return "", err
	}
	prefix := map[Type]string{TypeUser: "/people/", TypeArticle: "/news/", TypeIdea: "/ideas/"}
	if r.Type == TypeComment {
		if r.ParentID == nil {
			return "", apperr.ErrNotFound
		}
		p, err := rp.CanonicalPath(ctx, q, *r.ParentID)
		if err != nil {
			return "", err
		}
		return p + "#comment-" + r.ID.String(), nil
	}
	var slug string
	err = q.QueryRow(ctx, `SELECT slug FROM slugs WHERE entity_type = $1 AND entity_id = $2 AND is_current`, string(r.Type), id).Scan(&slug)
	if db.IsNoRows(err) {
		return "", apperr.ErrNotFound
	}
	return prefix[r.Type] + slug, err
}

// Reconcile rebuilds the registry from the entity tables and returns the number of rows inserted or changed.
func Reconcile(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	stmts := []string{
		`INSERT INTO resources (id, type, owner_id, status, visibility)
		 SELECT id, 'user', id, CASE status WHEN 'active' THEN 'active' WHEN 'deleted' THEN 'removed' ELSE 'hidden' END,
		        CASE WHEN profile_public THEN 'public' ELSE 'restricted' END FROM users
		 ON CONFLICT (id) DO UPDATE SET owner_id=EXCLUDED.owner_id, status=EXCLUDED.status, visibility=EXCLUDED.visibility, updated_at=now()
		 WHERE (resources.owner_id, resources.status, resources.visibility) IS DISTINCT FROM (EXCLUDED.owner_id, EXCLUDED.status, EXCLUDED.visibility)`,
		`INSERT INTO resources (id, type, owner_id, status, visibility)
		 SELECT id, 'article', author_id, CASE status WHEN 'published' THEN 'active' WHEN 'archived' THEN 'hidden' ELSE 'pending' END,
		        CASE WHEN status = 'published' THEN visibility ELSE 'private' END FROM articles
		 ON CONFLICT (id) DO UPDATE SET owner_id=EXCLUDED.owner_id, status=EXCLUDED.status, visibility=EXCLUDED.visibility, updated_at=now()
		 WHERE (resources.owner_id, resources.status, resources.visibility) IS DISTINCT FROM (EXCLUDED.owner_id, EXCLUDED.status, EXCLUDED.visibility)`,
		`INSERT INTO resources (id, type, owner_id, status, visibility) SELECT id, 'idea', author_id, 'active', 'public' FROM ideas
		 ON CONFLICT (id) DO UPDATE SET owner_id=EXCLUDED.owner_id, status=EXCLUDED.status, visibility=EXCLUDED.visibility, updated_at=now()
		 WHERE (resources.owner_id, resources.status, resources.visibility) IS DISTINCT FROM (EXCLUDED.owner_id, EXCLUDED.status, EXCLUDED.visibility)`,
		`INSERT INTO resources (id, type, owner_id, status, visibility, parent_id)
		 SELECT id, 'comment', author_id, CASE status WHEN 'hidden' THEN 'hidden' WHEN 'deleted' THEN 'removed' ELSE 'active' END, 'public', target_id FROM comments
		 ON CONFLICT (id) DO UPDATE SET owner_id=EXCLUDED.owner_id, status=EXCLUDED.status, visibility=EXCLUDED.visibility, parent_id=EXCLUDED.parent_id, updated_at=now()
		 WHERE (resources.owner_id, resources.status, resources.visibility, resources.parent_id) IS DISTINCT FROM (EXCLUDED.owner_id, EXCLUDED.status, EXCLUDED.visibility, EXCLUDED.parent_id)`,
	}
	var total int64
	for _, s := range stmts {
		tag, err := pool.Exec(ctx, s)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

// PublicNow reads the current state: the resource must be publicly visible and, for a comment, so must its parent
// (a closed article hides its comments at once, without waiting for the reconcile job).
func (r Repo) PublicNow(ctx context.Context, q db.DBTX, id uuid.UUID) (Resource, bool, error) {
	res, err := r.Get(ctx, q, id)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return Resource{}, false, nil
		}
		return Resource{}, false, err
	}
	if !res.PubliclyVisible() {
		return res, false, nil
	}
	if res.Type == TypeComment {
		if res.ParentID == nil {
			return res, false, nil
		}
		parent, err := r.Get(ctx, q, *res.ParentID)
		if err != nil {
			if errors.Is(err, apperr.ErrNotFound) {
				return res, false, nil
			}
			return res, false, err
		}
		return res, parent.PubliclyVisible(), nil
	}
	return res, true, nil
}

// Handler serves GET /resources/{id}.
type Handler struct {
	Pool *pgxpool.Pool
	Repo Repo
}

func (h *Handler) Routes(r chi.Router) { r.Get("/resources/{id}", h.get) }

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	res, ok, err := h.Repo.PublicNow(r.Context(), h.Pool, id)
	if err != nil || !ok {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	path, err := h.Repo.CanonicalPath(r.Context(), h.Pool, id)
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrNotFound)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{"type": res.Type, "canonical_path": path})
}
