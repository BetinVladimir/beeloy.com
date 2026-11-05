package articles

import (
	"context"
	"encoding/json"
	"time"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/pagination"
	"github.com/google/uuid"
)

type Repo struct{}

func (Repo) ListCategories(ctx context.Context, q db.DBTX) ([]Category, error) {
	rows, err := q.Query(ctx, `SELECT id, key, names, sort_order FROM categories ORDER BY sort_order, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanCategory(r interface{ Scan(...any) error }) (Category, error) {
	var c Category
	var names []byte
	if err := r.Scan(&c.ID, &c.Key, &names, &c.SortOrder); err != nil {
		return c, err
	}
	_ = json.Unmarshal(names, &c.Names)
	return c, nil
}

func (Repo) GetCategoryByKey(ctx context.Context, q db.DBTX, key string) (Category, error) {
	c, err := scanCategory(q.QueryRow(ctx, `SELECT id, key, names, sort_order FROM categories WHERE key = $1`, key))
	if db.IsNoRows(err) {
		return c, apperr.ErrNotFound
	}
	return c, err
}

func (Repo) GetCategoryByID(ctx context.Context, q db.DBTX, id uuid.UUID) (Category, error) {
	c, err := scanCategory(q.QueryRow(ctx, `SELECT id, key, names, sort_order FROM categories WHERE id = $1`, id))
	if db.IsNoRows(err) {
		return c, apperr.ErrNotFound
	}
	return c, err
}

func (Repo) InsertArticle(ctx context.Context, q db.DBTX, a Article) error {
	_, err := q.Exec(ctx, `INSERT INTO articles (id, category_id, author_id, source_locale, status, visibility, cover_url)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, a.ID, a.CategoryID, a.AuthorID, a.SourceLocale, a.Status, a.Visibility, a.CoverURL)
	return err
}

const articleSelect = `SELECT a.id, a.category_id, c.key, a.author_id, a.source_locale, a.status, a.visibility, a.rev, a.published_rev,
	a.cover_url, a.published_at, a.reviewed_at, a.reviewed_by, a.created_at, a.updated_at, COALESCE(s.slug,'')
	FROM articles a JOIN categories c ON c.id = a.category_id
	LEFT JOIN slugs s ON s.entity_type = 'article' AND s.entity_id = a.id AND s.is_current`

func scanArticle(r interface{ Scan(...any) error }) (Article, error) {
	var a Article
	err := r.Scan(&a.ID, &a.CategoryID, &a.CategoryKey, &a.AuthorID, &a.SourceLocale, &a.Status, &a.Visibility, &a.Rev, &a.PublishedRev,
		&a.CoverURL, &a.PublishedAt, &a.ReviewedAt, &a.ReviewedBy, &a.CreatedAt, &a.UpdatedAt, &a.Slug)
	return a, err
}

func (Repo) GetArticle(ctx context.Context, q db.DBTX, id uuid.UUID) (Article, error) {
	a, err := scanArticle(q.QueryRow(ctx, articleSelect+` WHERE a.id = $1`, id))
	if db.IsNoRows(err) {
		return a, apperr.ErrNotFound
	}
	return a, err
}

// ReserveRev atomically takes the next revision number (protects against two editors saving at once).
func (Repo) ReserveRev(ctx context.Context, q db.DBTX, id uuid.UUID) (int, error) {
	var rev int
	err := q.QueryRow(ctx, `UPDATE articles SET rev = rev + 1, updated_at = now() WHERE id = $1 RETURNING rev`, id).Scan(&rev)
	if db.IsNoRows(err) {
		return 0, apperr.ErrNotFound
	}
	return rev, err
}

func (Repo) UpdateMeta(ctx context.Context, q db.DBTX, id uuid.UUID, categoryID *uuid.UUID, setCover bool, coverURL *string) error {
	_, err := q.Exec(ctx, `UPDATE articles SET category_id = COALESCE($2, category_id),
		cover_url = CASE WHEN $3 THEN $4 ELSE cover_url END, updated_at = now() WHERE id = $1`, id, categoryID, setCover, coverURL)
	return err
}

// SetStatus changes status only if the current one is in from; reports whether a row was changed.
func (Repo) SetStatus(ctx context.Context, q db.DBTX, id uuid.UUID, from []ArticleStatus, to ArticleStatus) (bool, error) {
	f := make([]string, len(from))
	for i, s := range from {
		f[i] = string(s)
	}
	tag, err := q.Exec(ctx, `UPDATE articles SET status = $3, updated_at = now() WHERE id = $1 AND status = ANY($2)`, id, f, to)
	return tag.RowsAffected() == 1, err
}

// Approve is the moderator's decision: the approved revision, visibility and publication.
func (Repo) Approve(ctx context.Context, q db.DBTX, id uuid.UUID, rev int, vis Visibility, reviewer uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE articles SET published_rev = $2, status = 'published', visibility = $3, reviewed_by = $4, reviewed_at = now(),
		published_at = COALESCE(published_at, now()), updated_at = now() WHERE id = $1`, id, rev, vis, reviewer)
	return err
}

func (Repo) SetAccess(ctx context.Context, q db.DBTX, id uuid.UUID, userIDs []uuid.UUID) error {
	if _, err := q.Exec(ctx, `DELETE FROM article_access WHERE article_id = $1`, id); err != nil {
		return err
	}
	if len(userIDs) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO article_access (article_id, user_id) SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`, id, userIDs)
	return err
}

func (Repo) HasAccess(ctx context.Context, q db.DBTX, id, userID uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM article_access WHERE article_id = $1 AND user_id = $2)`, id, userID).Scan(&ok)
	return ok, err
}

func (Repo) ListAccess(ctx context.Context, q db.DBTX, id uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT user_id FROM article_access WHERE article_id = $1 ORDER BY user_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var u uuid.UUID
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountActiveUsers returns how many of ids are active users (validation of viewer lists).
func (Repo) CountActiveUsers(ctx context.Context, q db.DBTX, ids []uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = ANY($1) AND status = 'active'`, ids).Scan(&n)
	return n, err
}

// UpsertRevision registers a file version; existing (article, rev, locale) rows are kept (immutability).
// replace=true updates the row (translations that were replaced with --force or by a human).
func (Repo) UpsertRevision(ctx context.Context, q db.DBTX, r RevisionRow, searchText string, replace bool) (bool, error) {
	conflict := `ON CONFLICT (article_id, rev, locale) DO NOTHING`
	if replace {
		conflict = `ON CONFLICT (article_id, rev, locale) DO UPDATE SET origin = EXCLUDED.origin, model = EXCLUDED.model, title = EXCLUDED.title,
			summary = EXCLUDED.summary, sha256 = EXCLUDED.sha256, size = EXCLUDED.size, search_vector = EXCLUDED.search_vector, created_at = now()`
	}
	tag, err := q.Exec(ctx, `INSERT INTO article_revisions (article_id, rev, locale, origin, model, title, summary, sha256, size, search_vector)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, to_tsvector('simple', $10)) `+conflict,
		r.ArticleID, r.Rev, r.Locale, r.Origin, r.Model, r.Title, r.Summary, r.SHA256, r.Size, searchText)
	return tag.RowsAffected() == 1, err
}

func (Repo) GetRevision(ctx context.Context, q db.DBTX, id uuid.UUID, rev int, locale string) (RevisionRow, error) {
	var r RevisionRow
	err := q.QueryRow(ctx, `SELECT article_id, rev, locale, origin, model, title, summary, sha256, size, created_at FROM article_revisions
		WHERE article_id = $1 AND rev = $2 AND locale = $3`, id, rev, locale).
		Scan(&r.ArticleID, &r.Rev, &r.Locale, &r.Origin, &r.Model, &r.Title, &r.Summary, &r.SHA256, &r.Size, &r.CreatedAt)
	if db.IsNoRows(err) {
		return r, apperr.ErrNotFound
	}
	return r, err
}

func (Repo) ListRevisions(ctx context.Context, q db.DBTX, id uuid.UUID) ([]RevisionRow, error) {
	rows, err := q.Query(ctx, `SELECT article_id, rev, locale, origin, model, title, summary, sha256, size, created_at FROM article_revisions
		WHERE article_id = $1 ORDER BY rev DESC, locale`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RevisionRow{}
	for rows.Next() {
		var r RevisionRow
		if err := rows.Scan(&r.ArticleID, &r.Rev, &r.Locale, &r.Origin, &r.Model, &r.Title, &r.Summary, &r.SHA256, &r.Size, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RefreshLive rebuilds the live index of one article: for each locale exactly the file of rev = published_rev.
// A locale without a file for that revision is simply absent (readers fall back to en/source of the same revision).
func (Repo) RefreshLive(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	if _, err := q.Exec(ctx, `DELETE FROM article_locales WHERE article_id = $1 AND locale NOT IN (
		SELECT r.locale FROM article_revisions r JOIN articles a ON a.id = r.article_id
		WHERE r.article_id = $1 AND r.rev = a.published_rev)`, id); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `INSERT INTO article_locales (article_id, locale, rev, origin, model, title, summary, sha256, size, search_vector)
		SELECT r.article_id, r.locale, r.rev, r.origin, r.model, r.title, r.summary, r.sha256, r.size, r.search_vector
		FROM article_revisions r JOIN articles a ON a.id = r.article_id
		WHERE r.article_id = $1 AND r.rev = a.published_rev
		ON CONFLICT (article_id, locale) DO UPDATE SET rev = EXCLUDED.rev, origin = EXCLUDED.origin, model = EXCLUDED.model, title = EXCLUDED.title,
			summary = EXCLUDED.summary, sha256 = EXCLUDED.sha256, size = EXCLUDED.size, search_vector = EXCLUDED.search_vector, updated_at = now()`, id)
	return err
}

func (Repo) LiveLocales(ctx context.Context, q db.DBTX, id uuid.UUID) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT locale FROM article_locales WHERE article_id = $1 ORDER BY locale`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PickLive chooses the locale row to show: requested, then fallback, then the source locale (then any).
func (Repo) PickLive(ctx context.Context, q db.DBTX, id uuid.UUID, requested, fallback string) (LiveRow, bool, error) {
	var l LiveRow
	err := q.QueryRow(ctx, `SELECT al.article_id, al.locale, al.rev, al.origin, al.model, al.title, al.summary, al.sha256, al.size
		FROM article_locales al JOIN articles a ON a.id = al.article_id WHERE al.article_id = $1
		ORDER BY (al.locale = $2) DESC, (al.locale = $3) DESC, (al.locale = a.source_locale) DESC, al.locale LIMIT 1`, id, requested, fallback).
		Scan(&l.ArticleID, &l.Locale, &l.Rev, &l.Origin, &l.Model, &l.Title, &l.Summary, &l.SHA256, &l.Size)
	if db.IsNoRows(err) {
		return l, false, apperr.ErrNotFound
	}
	return l, l.Locale != requested, err
}

const listLateral = `JOIN LATERAL (SELECT al.locale, al.title, al.summary FROM article_locales al WHERE al.article_id = a.id
	ORDER BY (al.locale = $1) DESC, (al.locale = $2) DESC, (al.locale = a.source_locale) DESC, al.locale LIMIT 1) l ON true`

func scanListItem(rows interface{ Scan(...any) error }, requested string) (ArticleListItem, error) {
	var it ArticleListItem
	var pub *time.Time
	if err := rows.Scan(&it.ID, &it.Slug, &it.CategoryKey, &it.Locale, &it.Title, &it.Summary, &it.CoverURL, &pub); err != nil {
		return it, err
	}
	if pub != nil {
		it.PublishedAt = *pub
	}
	if it.Locale != requested {
		r := requested
		it.FallbackFrom = &r
	}
	return it, nil
}

// ListPublished returns public, published articles newest first; categoryKey keeps one category, excludeKey drops one.
func (Repo) ListPublished(ctx context.Context, q db.DBTX, locale, fallback string, categoryKey, excludeKey *string, cur pagination.Cursor, limit int) ([]ArticleListItem, error) {
	var curT *time.Time
	if !cur.IsZero() {
		curT = &cur.T
	}
	rows, err := q.Query(ctx, `SELECT a.id, s.slug, c.key, l.locale, l.title, l.summary, a.cover_url, a.published_at
		FROM articles a JOIN categories c ON c.id = a.category_id
		JOIN slugs s ON s.entity_type = 'article' AND s.entity_id = a.id AND s.is_current
		`+listLateral+`
		WHERE a.status = 'published' AND a.visibility = 'public' AND ($3::text IS NULL OR c.key = $3) AND ($4::text IS NULL OR c.key <> $4)
		  AND ($5::timestamptz IS NULL OR (a.published_at, a.id) < ($5, $6))
		ORDER BY a.published_at DESC, a.id DESC LIMIT $7`, locale, fallback, categoryKey, excludeKey, curT, cur.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArticleListItem{}
	for rows.Next() {
		it, err := scanListItem(rows, locale)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ListAccessibleRestricted returns restricted published articles the user was given access to.
func (Repo) ListAccessibleRestricted(ctx context.Context, q db.DBTX, userID uuid.UUID, locale, fallback string, cur pagination.Cursor, limit int) ([]ArticleListItem, error) {
	var curT *time.Time
	if !cur.IsZero() {
		curT = &cur.T
	}
	rows, err := q.Query(ctx, `SELECT a.id, s.slug, c.key, l.locale, l.title, l.summary, a.cover_url, a.published_at
		FROM articles a JOIN categories c ON c.id = a.category_id
		JOIN slugs s ON s.entity_type = 'article' AND s.entity_id = a.id AND s.is_current
		JOIN article_access ac ON ac.article_id = a.id AND ac.user_id = $3
		`+listLateral+`
		WHERE a.status = 'published' AND a.visibility = 'restricted'
		  AND ($4::timestamptz IS NULL OR (a.published_at, a.id) < ($4, $5))
		ORDER BY a.published_at DESC, a.id DESC LIMIT $6`, locale, fallback, userID, curT, cur.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArticleListItem{}
	for rows.Next() {
		it, err := scanListItem(rows, locale)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// AuthorInfo returns nickname and slug of an author ("Deleted user", "" when gone or anonymized).
func (Repo) AuthorInfo(ctx context.Context, q db.DBTX, id *uuid.UUID) (string, string, error) {
	if id == nil {
		return "Deleted user", "", nil
	}
	var nick, slug string
	var status string
	err := q.QueryRow(ctx, `SELECT u.nickname, u.status, COALESCE(s.slug,'') FROM users u
		LEFT JOIN slugs s ON s.entity_type='user' AND s.entity_id=u.id AND s.is_current WHERE u.id = $1`, *id).Scan(&nick, &status, &slug)
	if db.IsNoRows(err) || status == "deleted" {
		return "Deleted user", "", nil
	}
	return nick, slug, err
}

const editorListSelect = `SELECT a.id, COALESCE(s.slug,''), c.key, a.source_locale, a.status, a.visibility, a.rev, a.published_rev,
	COALESCE((SELECT r.title FROM article_revisions r WHERE r.article_id = a.id AND r.locale = a.source_locale ORDER BY r.rev DESC LIMIT 1), ''),
	(a.status = 'in_review' OR (a.status = 'published' AND a.rev > a.published_rev)), a.updated_at
	FROM articles a JOIN categories c ON c.id = a.category_id
	LEFT JOIN slugs s ON s.entity_type = 'article' AND s.entity_id = a.id AND s.is_current`

func scanEditorItems(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}) ([]EditorListItem, error) {
	defer rows.Close()
	out := []EditorListItem{}
	for rows.Next() {
		var it EditorListItem
		if err := rows.Scan(&it.ID, &it.Slug, &it.CategoryKey, &it.SourceLocale, &it.Status, &it.Visibility, &it.Rev, &it.PublishedRev, &it.Title, &it.Pending, &it.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ListForEditor lists articles incl. drafts; categoryIDs == nil means all categories.
func (Repo) ListForEditor(ctx context.Context, q db.DBTX, categoryIDs []uuid.UUID, all bool, cur pagination.Cursor, limit int) ([]EditorListItem, error) {
	var curT *time.Time
	if !cur.IsZero() {
		curT = &cur.T
	}
	rows, err := q.Query(ctx, editorListSelect+` WHERE ($1 OR a.category_id = ANY($2)) AND ($3::timestamptz IS NULL OR (a.updated_at, a.id) < ($3, $4))
		ORDER BY a.updated_at DESC, a.id DESC LIMIT $5`, all, categoryIDs, curT, cur.ID, limit)
	if err != nil {
		return nil, err
	}
	return scanEditorItems(rows)
}

// ListReviewQueue lists articles waiting for a moderator: in_review, or published with a newer revision.
func (Repo) ListReviewQueue(ctx context.Context, q db.DBTX, cur pagination.Cursor, limit int) ([]EditorListItem, error) {
	var curT *time.Time
	if !cur.IsZero() {
		curT = &cur.T
	}
	rows, err := q.Query(ctx, editorListSelect+` WHERE (a.status = 'in_review' OR (a.status = 'published' AND a.rev > a.published_rev))
		AND ($1::timestamptz IS NULL OR (a.updated_at, a.id) < ($1, $2)) ORDER BY a.updated_at DESC, a.id DESC LIMIT $3`, curT, cur.ID, limit)
	if err != nil {
		return nil, err
	}
	return scanEditorItems(rows)
}

// UpsertFromManifest is used by Content Sync. A new article becomes in_review; an existing one keeps its
// status, visibility and published_rev (only descriptive fields and rev are updated).
func (Repo) UpsertFromManifest(ctx context.Context, q db.DBTX, m Manifest, categoryID uuid.UUID) (bool, error) {
	var created bool
	err := q.QueryRow(ctx, `INSERT INTO articles (id, category_id, author_id, source_locale, status, rev, cover_url, created_at, updated_at)
		VALUES ($1,$2,$3,$4,'in_review',$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE SET category_id = EXCLUDED.category_id, source_locale = EXCLUDED.source_locale,
			cover_url = EXCLUDED.cover_url, rev = GREATEST(articles.rev, EXCLUDED.rev), updated_at = now()
		RETURNING (xmax = 0)`, m.ArticleID, categoryID, m.AuthorID, m.SourceLocale, m.Rev, m.CoverURL, m.CreatedAt, m.UpdatedAt).Scan(&created)
	return created, err
}

// SetFingerprint records the S3 state that the sync has processed.
func (Repo) SetFingerprint(ctx context.Context, q db.DBTX, id uuid.UUID, fp string) error {
	_, err := q.Exec(ctx, `UPDATE articles SET s3_fingerprint = $2, synced_at = now() WHERE id = $1`, id, fp)
	return err
}

func (Repo) Fingerprint(ctx context.Context, q db.DBTX, id uuid.UUID) (string, error) {
	var fp string
	err := q.QueryRow(ctx, `SELECT s3_fingerprint FROM articles WHERE id = $1`, id).Scan(&fp)
	if db.IsNoRows(err) {
		return "", nil
	}
	return fp, err
}

// SitemapEntry is one public article for the sitemap.
type SitemapEntry struct {
	Slug      string    `json:"path_slug"`
	UpdatedAt time.Time `json:"updated_at"`
	Locales   []string  `json:"locales"`
	Category  string    `json:"category_key"`
	ID        uuid.UUID `json:"-"`
}

func (Repo) SitemapEntries(ctx context.Context, q db.DBTX, cur pagination.Cursor, limit int) ([]SitemapEntry, error) {
	var curT *time.Time
	if !cur.IsZero() {
		curT = &cur.T
	}
	rows, err := q.Query(ctx, `SELECT a.id, s.slug, a.updated_at, c.key, COALESCE(array_agg(al.locale ORDER BY al.locale) FILTER (WHERE al.locale IS NOT NULL), '{}')
		FROM articles a JOIN categories c ON c.id = a.category_id JOIN slugs s ON s.entity_type='article' AND s.entity_id=a.id AND s.is_current
		LEFT JOIN article_locales al ON al.article_id = a.id
		WHERE a.status='published' AND a.visibility='public' AND ($1::timestamptz IS NULL OR (a.updated_at, a.id) < ($1, $2))
		GROUP BY a.id, s.slug, a.updated_at, c.key ORDER BY a.updated_at DESC, a.id DESC LIMIT $3`, curT, cur.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SitemapEntry{}
	for rows.Next() {
		var e SitemapEntry
		if err := rows.Scan(&e.ID, &e.Slug, &e.UpdatedAt, &e.Category, &e.Locales); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
