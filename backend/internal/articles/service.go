package articles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/ids"
	"beeloy.com/backend/internal/platform/outbox"
	"beeloy.com/backend/internal/platform/pagination"
	"beeloy.com/backend/internal/platform/s3store"
	"beeloy.com/backend/internal/registry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type Service struct {
	Pool     *pgxpool.Pool
	Repo     Repo
	Store    s3store.ObjectStore
	Slugs    *identity.SlugService
	Authz    acl.Authorizer
	RDB      *redis.Client
	Audit    audit.Service
	Registry registry.Repo
	Kick     func() // wakes the outbox relay (may be nil)
}

func (s *Service) kick() {
	if s.Kick != nil {
		s.Kick()
	}
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (s *Service) storeErr(err error) error {
	return apperr.ErrUnavailable.Wrap(err)
}

func (s *Service) putJSON(ctx context.Context, key string, v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if err := s.Store.Put(ctx, key, raw, "application/json"); err != nil {
		return nil, s.storeErr(err)
	}
	return raw, nil
}

// writeManifest creates the manifest (PutIfAbsent) or updates it by compare-and-swap: concurrent editors never
// overwrite each other and rev never decreases. Revision files are immutable; only this pointer changes.
func (s *Service) writeManifest(ctx context.Context, a Article) error {
	key := ManifestKey(a.ID)
	for attempt := 0; attempt < 4; attempt++ {
		raw, etag, err := s.Store.GetWithETag(ctx, key, MaxManifestBytes)
		m := Manifest{SchemaVersion: 1, ArticleID: a.ID, Slug: a.Slug, CategoryKey: a.CategoryKey, SourceLocale: a.SourceLocale,
			Rev: a.Rev, CoverURL: a.CoverURL, AuthorID: a.AuthorID, CreatedAt: a.CreatedAt.UTC()}
		switch {
		case errors.Is(err, s3store.ErrNotFound):
			m.UpdatedAt = time.Now().UTC()
			out, _ := json.Marshal(m)
			switch perr := s.Store.PutIfAbsent(ctx, key, out, "application/json"); {
			case perr == nil:
				return nil
			case errors.Is(perr, s3store.ErrConflict):
				continue
			default:
				return s.storeErr(perr)
			}
		case err != nil:
			return s.storeErr(err)
		}
		var cur Manifest
		if json.Unmarshal(raw, &cur) != nil {
			return apperr.ErrInternal.WithMessage("corrupt manifest")
		}
		cur.CategoryKey, cur.CoverURL = a.CategoryKey, a.CoverURL
		if a.Rev > cur.Rev {
			cur.Rev = a.Rev // never decreased
		}
		cur.UpdatedAt = time.Now().UTC()
		out, _ := json.Marshal(cur)
		switch perr := s.Store.PutIfMatch(ctx, key, out, "application/json", etag); {
		case perr == nil:
			return nil
		case errors.Is(perr, s3store.ErrConflict):
			continue
		default:
			return s.storeErr(perr)
		}
	}
	return apperr.Conflict("manifest_conflict")
}

func validCover(u *string) error {
	if u == nil || *u == "" {
		return nil
	}
	p, err := url.Parse(*u)
	if err != nil || p.Scheme != "https" || p.Host == "" || len(*u) > 500 {
		return apperr.Field("cover_url", "invalid_format")
	}
	return nil
}

func (s *Service) syncRegistry(ctx context.Context, q db.DBTX, a Article) error {
	return s.Registry.Upsert(ctx, q, registry.FromArticle(a.ID, a.AuthorID, string(a.Status), string(a.Visibility)))
}

func (s *Service) getWithRight(ctx context.Context, subj acl.Subject, id uuid.UUID, perm acl.Perm) (Article, error) {
	a, err := s.Repo.GetArticle(ctx, s.Pool, id)
	if err != nil {
		return a, err
	}
	if !s.Authz.Can(subj, perm, acl.Resource{CategoryID: &a.CategoryID}) {
		// do not reveal articles to people without any right to them
		if !s.Authz.Can(subj, acl.PermArticleWrite, acl.Resource{CategoryID: &a.CategoryID}) && !s.Authz.Can(subj, acl.PermArticlePublish, acl.Resource{CategoryID: &a.CategoryID}) {
			return a, apperr.ErrNotFound
		}
		return a, apperr.ErrForbidden
	}
	return a, nil
}

// Create makes a draft. The manifest is written to S3 first; an orphan manifest left by a DB failure is harmless
// (Content Sync registers it as in_review).
func (s *Service) Create(ctx context.Context, subj acl.Subject, req CreateArticleReq) (Article, error) {
	cat, err := s.Repo.GetCategoryByID(ctx, s.Pool, req.CategoryID)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return Article{}, apperr.Field("category_id", "invalid_value")
		}
		return Article{}, err
	}
	if err := s.Authz.Require(&subj, acl.PermArticleWrite, acl.Resource{CategoryID: &cat.ID}); err != nil {
		return Article{}, err
	}
	if !locales.Known(req.SourceLocale) {
		return Article{}, apperr.Field("source_locale", "invalid_value")
	}
	if err := validCover(req.CoverURL); err != nil {
		return Article{}, err
	}
	slug := identity.Normalize(req.Slug)
	if slug != "" {
		if err := identity.Validate(identity.EntityArticle, slug); err != nil {
			return Article{}, err
		}
		ok, err := s.Slugs.IsAvailable(ctx, s.Pool, identity.EntityArticle, slug)
		if err != nil {
			return Article{}, err
		}
		if !ok {
			return Article{}, apperr.Conflict("slug_taken")
		}
	} else {
		if strings.TrimSpace(req.TitleForSlug) == "" {
			return Article{}, apperr.Field("title_for_slug", "required")
		}
		if slug, err = s.Slugs.GenerateUnique(ctx, s.Pool, identity.EntityArticle, req.TitleForSlug); err != nil {
			return Article{}, err
		}
	}
	a := Article{ID: ids.New(), CategoryID: cat.ID, CategoryKey: cat.Key, AuthorID: &subj.UserID, SourceLocale: req.SourceLocale,
		Status: StatusDraft, Visibility: VisPublic, CoverURL: req.CoverURL, CreatedAt: time.Now(), Slug: slug}
	if err := s.writeManifest(ctx, a); err != nil {
		return Article{}, err
	}
	err = db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.Repo.InsertArticle(ctx, tx, a); err != nil {
			return err
		}
		if err := s.Slugs.Assign(ctx, tx, identity.EntityArticle, a.ID, slug); err != nil {
			return err
		}
		return s.syncRegistry(ctx, tx, a)
	})
	if err != nil {
		return Article{}, err
	}
	return s.Repo.GetArticle(ctx, s.Pool, a.ID)
}

func (s *Service) UpdateMeta(ctx context.Context, subj acl.Subject, id uuid.UUID, req UpdateArticleReq) (Article, error) {
	a, err := s.getWithRight(ctx, subj, id, acl.PermArticleWrite)
	if err != nil {
		return a, err
	}
	var newCat *uuid.UUID
	if req.CategoryID != nil && *req.CategoryID != a.CategoryID {
		c, err := s.Repo.GetCategoryByID(ctx, s.Pool, *req.CategoryID)
		if err != nil {
			return a, apperr.Field("category_id", "invalid_value")
		}
		if err := s.Authz.Require(&subj, acl.PermArticleWrite, acl.Resource{CategoryID: &c.ID}); err != nil {
			return a, err
		}
		newCat = &c.ID
	}
	if err := validCover(req.CoverURL); err != nil {
		return a, err
	}
	setCover := req.CoverURL != nil || req.ClearCover
	var cover *string
	if req.CoverURL != nil && !req.ClearCover {
		cover = req.CoverURL
	}
	if err := s.Repo.UpdateMeta(ctx, s.Pool, id, newCat, setCover, cover); err != nil {
		return a, err
	}
	a, err = s.Repo.GetArticle(ctx, s.Pool, id)
	if err != nil {
		return a, err
	}
	return a, s.writeManifest(ctx, a)
}

func (s *Service) toRevision(f LocaleFile, raw []byte) RevisionRow {
	return RevisionRow{ArticleID: f.ArticleID, Rev: f.Rev, Locale: f.Locale, Origin: f.Origin, Model: f.Model, Title: f.Title, Summary: f.Summary, SHA256: sha(raw), Size: len(raw)}
}

func (s *Service) registerRevision(ctx context.Context, a Article, f LocaleFile, raw []byte, replace bool) error {
	return db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := s.Repo.UpsertRevision(ctx, tx, s.toRevision(f, raw), SearchText(f), replace); err != nil {
			return err
		}
		if a.PublishedRev > 0 && f.Rev == a.PublishedRev {
			if err := s.Repo.RefreshLive(ctx, tx, a.ID); err != nil {
				return err
			}
		}
		return outbox.Add(ctx, tx, outbox.Event{Type: "article.revision_registered", AggregateType: "article", AggregateID: a.ID,
			Payload: map[string]any{"rev": f.Rev, "locale": f.Locale}})
	})
}

// SaveSource stores the source text as a NEW revision; what readers see is not changed.
func (s *Service) SaveSource(ctx context.Context, subj acl.Subject, id uuid.UUID, req TextReq) (int, error) {
	a, err := s.getWithRight(ctx, subj, id, acl.PermArticleWrite)
	if err != nil {
		return 0, err
	}
	if err := ValidateTexts(req.Title, req.Summary, req.BodyMD); err != nil {
		return 0, err
	}
	rev, err := s.Repo.ReserveRev(ctx, s.Pool, id)
	if err != nil {
		return 0, err
	}
	f := LocaleFile{SchemaVersion: 1, ArticleID: id, Locale: a.SourceLocale, Rev: rev, Origin: "human", Title: req.Title, Summary: req.Summary, BodyMD: req.BodyMD, GeneratedAt: time.Now().UTC()}
	raw, err := json.Marshal(f)
	if err != nil {
		return 0, err
	}
	if err := ValidateLocaleFile(f, raw, id, a.SourceLocale, rev); err != nil {
		return 0, err
	}
	// atomic create: an existing revision file is never replaced, whatever its origin
	if err := s.Store.PutIfAbsent(ctx, LocaleKey(id, rev, a.SourceLocale), raw, "application/json"); err != nil {
		if errors.Is(err, s3store.ErrConflict) {
			return 0, apperr.Conflict("rev_exists")
		}
		return 0, s.storeErr(err)
	}
	a.Rev = rev
	if err := s.writeManifest(ctx, a); err != nil {
		return 0, err
	}
	if err := s.registerRevision(ctx, a, f, raw, false); err != nil {
		return 0, err
	}
	s.kick()
	return rev, nil
}

// PutTranslation stores a human translation for revision rev (default: the latest). An existing file is never replaced.
func (s *Service) PutTranslation(ctx context.Context, subj acl.Subject, id uuid.UUID, rev int, locale string, req TextReq) error {
	a, err := s.getWithRight(ctx, subj, id, acl.PermArticleWrite)
	if err != nil {
		return err
	}
	if !locales.Known(locale) || locale == a.SourceLocale {
		return apperr.Field("locale", "invalid_value")
	}
	if rev == 0 {
		rev = a.Rev
	}
	if rev < 1 || rev > a.Rev {
		return apperr.Field("rev", "invalid_value")
	}
	if _, err := s.Repo.GetRevision(ctx, s.Pool, id, rev, a.SourceLocale); err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return apperr.Conflict("no_source_text")
		}
		return err
	}
	if _, err := s.Repo.GetRevision(ctx, s.Pool, id, rev, locale); err == nil {
		return apperr.Conflict("exists") // translations are immutable; a correction is a new source revision
	}
	f := LocaleFile{SchemaVersion: 1, ArticleID: id, Locale: locale, Rev: rev, Origin: "human", Title: req.Title, Summary: req.Summary, BodyMD: req.BodyMD, GeneratedAt: time.Now().UTC()}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := ValidateLocaleFile(f, raw, id, locale, rev); err != nil {
		return err
	}
	if err := s.Store.PutIfAbsent(ctx, LocaleKey(id, rev, locale), raw, "application/json"); err != nil {
		if errors.Is(err, s3store.ErrConflict) {
			return apperr.Conflict("exists")
		}
		return s.storeErr(err)
	}
	if err := s.registerRevision(ctx, a, f, raw, false); err != nil {
		return err
	}
	s.kick()
	return nil
}

func (s *Service) hasSource(ctx context.Context, a Article, rev int) (bool, error) {
	_, err := s.Repo.GetRevision(ctx, s.Pool, a.ID, rev, a.SourceLocale)
	if errors.Is(err, apperr.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Submit moves a draft to review.
func (s *Service) Submit(ctx context.Context, subj acl.Subject, id uuid.UUID) (Article, error) {
	a, err := s.getWithRight(ctx, subj, id, acl.PermArticleWrite)
	if err != nil {
		return a, err
	}
	if a.Rev < 1 {
		return a, apperr.Conflict("no_source_text")
	}
	if ok, err := s.hasSource(ctx, a, a.Rev); err != nil {
		return a, err
	} else if !ok {
		return a, apperr.Conflict("no_source_text")
	}
	err = db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		ok, err := s.Repo.SetStatus(ctx, tx, id, []ArticleStatus{StatusDraft}, StatusInReview)
		if err != nil {
			return err
		}
		if !ok {
			return apperr.Conflict("invalid_state")
		}
		a.Status = StatusInReview
		if err := s.syncRegistry(ctx, tx, a); err != nil {
			return err
		}
		return outbox.Add(ctx, tx, outbox.Event{Type: "article.submitted", AggregateType: "article", AggregateID: id, Payload: map[string]any{"actor_id": subj.UserID}})
	})
	if err != nil {
		return a, err
	}
	s.kick()
	return s.Repo.GetArticle(ctx, s.Pool, id)
}

// Publish is the moderator's approval of revision rev with a visibility.
func (s *Service) Publish(ctx context.Context, subj acl.Subject, ip netip.Addr, id uuid.UUID, rev int, vis Visibility, viewerIDs []uuid.UUID) (Article, error) {
	a, err := s.getWithRight(ctx, subj, id, acl.PermArticlePublish)
	if err != nil {
		return a, err
	}
	if a.Rev < 1 {
		return a, apperr.Conflict("no_source_text")
	}
	if vis != VisPublic && vis != VisRestricted {
		return a, apperr.Field("visibility", "invalid_value")
	}
	if rev < 1 || rev > a.Rev {
		return a, apperr.Field("rev", "invalid_value")
	}
	if ok, err := s.hasSource(ctx, a, rev); err != nil {
		return a, err
	} else if !ok {
		return a, apperr.Conflict("no_source_text")
	}
	if vis == VisRestricted {
		seen := map[uuid.UUID]bool{}
		uniq := viewerIDs[:0:0]
		for _, v := range viewerIDs {
			if !seen[v] {
				seen[v] = true
				uniq = append(uniq, v)
			}
		}
		viewerIDs = uniq
		if len(viewerIDs) > 0 {
			n, err := s.Repo.CountActiveUsers(ctx, s.Pool, viewerIDs)
			if err != nil {
				return a, err
			}
			if n != len(viewerIDs) {
				return a, apperr.Field("viewer_ids", "invalid_value")
			}
		}
	} else {
		viewerIDs = nil
	}
	err = db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.Repo.Approve(ctx, tx, id, rev, vis, subj.UserID); err != nil {
			return err
		}
		if err := s.Repo.SetAccess(ctx, tx, id, viewerIDs); err != nil {
			return err
		}
		if err := s.Repo.RefreshLive(ctx, tx, id); err != nil {
			return err
		}
		a.Status, a.Visibility = StatusPublished, vis
		if err := s.syncRegistry(ctx, tx, a); err != nil {
			return err
		}
		if err := outbox.Add(ctx, tx, outbox.Event{Type: "article.published", AggregateType: "article", AggregateID: id,
			Payload: map[string]any{"rev": rev, "visibility": vis, "actor_id": subj.UserID}}); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Input{ActorID: &subj.UserID, Action: audit.ActionArticlePublish, TargetType: "article", TargetID: id.String(),
			Meta: map[string]any{"rev": rev, "visibility": vis}, IP: ip})
	})
	if err != nil {
		return a, err
	}
	s.kick()
	return s.Repo.GetArticle(ctx, s.Pool, id)
}

func (s *Service) transition(ctx context.Context, subj acl.Subject, ip netip.Addr, id uuid.UUID, from []ArticleStatus, to ArticleStatus, action, evType string, meta map[string]any) (Article, error) {
	a, err := s.getWithRight(ctx, subj, id, acl.PermArticlePublish)
	if err != nil {
		return a, err
	}
	err = db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		ok, err := s.Repo.SetStatus(ctx, tx, id, from, to)
		if err != nil {
			return err
		}
		if !ok {
			return apperr.Conflict("invalid_state")
		}
		a.Status = to
		if err := s.syncRegistry(ctx, tx, a); err != nil {
			return err
		}
		if evType != "" {
			if err := outbox.Add(ctx, tx, outbox.Event{Type: evType, AggregateType: "article", AggregateID: id, Payload: map[string]any{"actor_id": subj.UserID}}); err != nil {
				return err
			}
		}
		return s.Audit.Record(ctx, tx, audit.Input{ActorID: &subj.UserID, Action: action, TargetType: "article", TargetID: id.String(), Meta: meta, IP: ip})
	})
	if err != nil {
		return a, err
	}
	s.kick()
	return s.Repo.GetArticle(ctx, s.Pool, id)
}

// Unpublish: published -> archived.
func (s *Service) Unpublish(ctx context.Context, subj acl.Subject, ip netip.Addr, id uuid.UUID) (Article, error) {
	return s.transition(ctx, subj, ip, id, []ArticleStatus{StatusPublished}, StatusArchived, audit.ActionArticleUnpublish, "article.unpublished", nil)
}

// Reject: in_review -> draft (the note is kept in the audit event only).
func (s *Service) Reject(ctx context.Context, subj acl.Subject, ip netip.Addr, id uuid.UUID, note string) (Article, error) {
	if len([]rune(note)) > 500 {
		return Article{}, apperr.Field("note", "too_long")
	}
	return s.transition(ctx, subj, ip, id, []ArticleStatus{StatusInReview}, StatusDraft, audit.ActionArticleReject, "", map[string]any{"note": note})
}

// ---- reads ----

func (s *Service) Categories(ctx context.Context) ([]Category, error) {
	return s.Repo.ListCategories(ctx, s.Pool)
}

func (s *Service) List(ctx context.Context, locale string, categoryKey *string, cur pagination.Cursor, limit int) ([]ArticleListItem, string, error) {
	if !locales.Known(locale) {
		return nil, "", apperr.Field("locale", "invalid_value")
	}
	items, err := s.Repo.ListPublished(ctx, s.Pool, locale, locales.Fallback(), categoryKey, cur, limit+1)
	if err != nil {
		return nil, "", err
	}
	return s.page(items, limit)
}

func (s *Service) page(items []ArticleListItem, limit int) ([]ArticleListItem, string, error) {
	page, more := pagination.Page(items, limit)
	next := ""
	if more {
		last := page[len(page)-1]
		next = pagination.Encode(pagination.Cursor{T: last.PublishedAt, ID: last.ID})
	}
	return page, next, nil
}

func (s *Service) ListMyRestricted(ctx context.Context, subj acl.Subject, locale string, cur pagination.Cursor, limit int) ([]ArticleListItem, string, error) {
	if !locales.Known(locale) {
		return nil, "", apperr.Field("locale", "invalid_value")
	}
	items, err := s.Repo.ListAccessibleRestricted(ctx, s.Pool, subj.UserID, locale, locales.Fallback(), cur, limit+1)
	if err != nil {
		return nil, "", err
	}
	return s.page(items, limit)
}

func (s *Service) loadBody(ctx context.Context, row LiveRow) (LocaleFile, error) {
	var f LocaleFile
	key := "art:" + row.SHA256
	var raw []byte
	if s.RDB != nil {
		if b, err := s.RDB.Get(ctx, key).Bytes(); err == nil {
			raw = b
		}
	}
	if raw == nil {
		b, err := s.Store.Get(ctx, LocaleKey(row.ArticleID, row.Rev, row.Locale), MaxLocaleFileBytes)
		if err != nil {
			return f, s.storeErr(err)
		}
		if sha(b) != row.SHA256 {
			return f, apperr.ErrInternal.Wrap(errors.New("s3 integrity: sha256 mismatch for " + LocaleKey(row.ArticleID, row.Rev, row.Locale)))
		}
		raw = b
		if s.RDB != nil {
			_ = s.RDB.Set(ctx, key, raw, time.Hour).Err()
		}
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, apperr.ErrInternal.Wrap(err)
	}
	return f, nil
}

func (s *Service) canSeeRestricted(ctx context.Context, viewer *acl.Subject, a Article) bool {
	if viewer == nil {
		return false
	}
	res := acl.Resource{CategoryID: &a.CategoryID}
	if s.Authz.Can(*viewer, acl.PermArticleWrite, res) || s.Authz.Can(*viewer, acl.PermArticlePublish, res) {
		return true
	}
	if viewer.Status != "active" {
		return false
	}
	ok, _ := s.Repo.HasAccess(ctx, s.Pool, a.ID, viewer.UserID)
	return ok
}

// GetBySlug returns the published view (the approved revision) and the canonical slug.
func (s *Service) GetBySlug(ctx context.Context, viewer *acl.Subject, slug, locale string) (ArticleView, string, error) {
	if !locales.Known(locale) {
		return ArticleView{}, "", apperr.Field("locale", "invalid_value")
	}
	id, canonical, err := s.Slugs.Resolve(ctx, s.Pool, identity.EntityArticle, slug)
	if err != nil {
		return ArticleView{}, "", err
	}
	a, err := s.Repo.GetArticle(ctx, s.Pool, id)
	if err != nil || a.Status != StatusPublished {
		return ArticleView{}, "", apperr.ErrNotFound
	}
	if a.Visibility == VisRestricted && !s.canSeeRestricted(ctx, viewer, a) {
		return ArticleView{}, "", apperr.ErrNotFound
	}
	row, fell, err := s.Repo.PickLive(ctx, s.Pool, id, locale, locales.Fallback())
	if err != nil {
		return ArticleView{}, "", err
	}
	f, err := s.loadBody(ctx, row)
	if err != nil {
		return ArticleView{}, "", err
	}
	avail, err := s.Repo.LiveLocales(ctx, s.Pool, id)
	if err != nil {
		return ArticleView{}, "", err
	}
	an, as, err := s.Repo.AuthorInfo(ctx, s.Pool, a.AuthorID)
	if err != nil {
		return ArticleView{}, "", err
	}
	v := ArticleView{ID: id, Slug: canonical, CategoryKey: a.CategoryKey, Locale: row.Locale, Title: f.Title, Summary: f.Summary, BodyMD: f.BodyMD,
		Visibility: a.Visibility, CoverURL: a.CoverURL, AuthorName: an, AuthorSlug: as, Origin: row.Origin, AvailableLocales: avail}
	if a.PublishedAt != nil {
		v.PublishedAt = *a.PublishedAt
	}
	if fell {
		l := locale
		v.FallbackFrom = &l
	}
	return v, canonical, nil
}

// ---- editor / moderation ----

func (s *Service) editorScope(subj acl.Subject) (all bool, cats []uuid.UUID, err error) {
	if s.Authz.Can(subj, acl.PermArticleWrite, acl.Resource{}) {
		return true, nil, nil
	}
	for _, b := range subj.Bindings {
		if b.Role == acl.RoleEditor && b.ScopeType == acl.ScopeCategory && b.ScopeID != nil && subj.Status == "active" {
			cats = append(cats, *b.ScopeID)
		}
	}
	if len(cats) == 0 {
		return false, nil, apperr.ErrForbidden
	}
	return false, cats, nil
}

func editorPage(items []EditorListItem, limit int) ([]EditorListItem, string) {
	page, more := pagination.Page(items, limit)
	next := ""
	if more {
		last := page[len(page)-1]
		next = pagination.Encode(pagination.Cursor{T: last.UpdatedAt, ID: last.ID})
	}
	return page, next
}

func (s *Service) ListForEditor(ctx context.Context, subj acl.Subject, cur pagination.Cursor, limit int) ([]EditorListItem, string, error) {
	all, cats, err := s.editorScope(subj)
	if err != nil {
		return nil, "", err
	}
	items, err := s.Repo.ListForEditor(ctx, s.Pool, cats, all, cur, limit+1)
	if err != nil {
		return nil, "", err
	}
	p, n := editorPage(items, limit)
	return p, n, nil
}

func (s *Service) ReviewQueue(ctx context.Context, subj acl.Subject, cur pagination.Cursor, limit int) ([]EditorListItem, string, error) {
	if err := s.Authz.Require(&subj, acl.PermArticlePublish, acl.Resource{}); err != nil {
		return nil, "", err
	}
	items, err := s.Repo.ListReviewQueue(ctx, s.Pool, cur, limit+1)
	if err != nil {
		return nil, "", err
	}
	p, n := editorPage(items, limit)
	return p, n, nil
}

func (s *Service) readFile(ctx context.Context, row RevisionRow) (LocaleFile, error) {
	var f LocaleFile
	b, err := s.Store.Get(ctx, LocaleKey(row.ArticleID, row.Rev, row.Locale), MaxLocaleFileBytes)
	if err != nil {
		return f, s.storeErr(err)
	}
	if sha(b) != row.SHA256 {
		return f, apperr.ErrInternal.Wrap(errors.New("s3 integrity: sha256 mismatch"))
	}
	return f, json.Unmarshal(b, &f)
}

// GetForEditor returns the article with all registered versions and the latest source text.
func (s *Service) GetForEditor(ctx context.Context, subj acl.Subject, id uuid.UUID) (EditorArticle, error) {
	a, err := s.Repo.GetArticle(ctx, s.Pool, id)
	if err != nil {
		return EditorArticle{}, err
	}
	res := acl.Resource{CategoryID: &a.CategoryID}
	if !s.Authz.Can(subj, acl.PermArticleWrite, res) && !s.Authz.Can(subj, acl.PermArticlePublish, res) {
		return EditorArticle{}, apperr.ErrNotFound
	}
	revs, err := s.Repo.ListRevisions(ctx, s.Pool, id)
	if err != nil {
		return EditorArticle{}, err
	}
	out := EditorArticle{Article: a, Revisions: revs}
	for i := range out.Revisions {
		out.Revisions[i].Stale = out.Revisions[i].Rev < a.Rev
	}
	for _, r := range revs { // revs are rev DESC
		if r.Locale == a.SourceLocale {
			if f, err := s.readFile(ctx, r); err == nil {
				out.Source = &f
			}
			break
		}
	}
	if out.ViewerIDs, err = s.Repo.ListAccess(ctx, s.Pool, id); err != nil {
		return out, err
	}
	return out, nil
}

// GetRevisionText returns any registered version for preview (same access rule as GetForEditor).
func (s *Service) GetRevisionText(ctx context.Context, subj acl.Subject, id uuid.UUID, rev int, locale string) (LocaleFile, error) {
	a, err := s.Repo.GetArticle(ctx, s.Pool, id)
	if err != nil {
		return LocaleFile{}, err
	}
	res := acl.Resource{CategoryID: &a.CategoryID}
	if !s.Authz.Can(subj, acl.PermArticleWrite, res) && !s.Authz.Can(subj, acl.PermArticlePublish, res) {
		return LocaleFile{}, apperr.ErrNotFound
	}
	row, err := s.Repo.GetRevision(ctx, s.Pool, id, rev, locale)
	if err != nil {
		return LocaleFile{}, err
	}
	return s.readFile(ctx, row)
}
