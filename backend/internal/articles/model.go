package articles

import (
	"time"

	"github.com/google/uuid"
)

type ArticleStatus string

const (
	StatusDraft     ArticleStatus = "draft"
	StatusInReview  ArticleStatus = "in_review"
	StatusPublished ArticleStatus = "published"
	StatusArchived  ArticleStatus = "archived"
)

type Visibility string

const (
	VisPublic     Visibility = "public"
	VisRestricted Visibility = "restricted"
)

type Category struct {
	ID        uuid.UUID         `json:"id"`
	Key       string            `json:"key"`
	Names     map[string]string `json:"names"`
	SortOrder int               `json:"-"`
}

type Article struct {
	ID           uuid.UUID     `json:"id"`
	CategoryID   uuid.UUID     `json:"category_id"`
	CategoryKey  string        `json:"category_key"`
	AuthorID     *uuid.UUID    `json:"author_id"`
	SourceLocale string        `json:"source_locale"`
	Status       ArticleStatus `json:"status"`
	Visibility   Visibility    `json:"visibility"`
	Rev          int           `json:"rev"`
	PublishedRev int           `json:"published_rev"`
	CoverURL     *string       `json:"cover_url"`
	PublishedAt  *time.Time    `json:"published_at"`
	ReviewedAt   *time.Time    `json:"reviewed_at"`
	ReviewedBy   *uuid.UUID    `json:"reviewed_by"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	Slug         string        `json:"slug"`
}

type RevisionRow struct {
	ArticleID uuid.UUID `json:"-"`
	Rev       int       `json:"rev"`
	Locale    string    `json:"locale"`
	Origin    string    `json:"origin"`
	Model     *string   `json:"model"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	SHA256    string    `json:"sha256"`
	Size      int       `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	Stale     bool      `json:"stale"`
}

type LiveRow struct {
	ArticleID uuid.UUID
	Locale    string
	Rev       int
	Origin    string
	Model     *string
	Title     string
	Summary   string
	SHA256    string
	Size      int
}

type ArticleListItem struct {
	ID           uuid.UUID `json:"id"`
	Slug         string    `json:"slug"`
	CategoryKey  string    `json:"category_key"`
	Locale       string    `json:"locale"`
	Title        string    `json:"title"`
	Summary      string    `json:"summary"`
	CoverURL     *string   `json:"cover_url"`
	PublishedAt  time.Time `json:"published_at"`
	FallbackFrom *string   `json:"fallback_from"`
}

type ArticleView struct {
	ID               uuid.UUID  `json:"id"`
	Slug             string     `json:"slug"`
	CategoryKey      string     `json:"category_key"`
	Locale           string     `json:"locale"`
	Title            string     `json:"title"`
	Summary          string     `json:"summary"`
	BodyMD           string     `json:"body_md"`
	Visibility       Visibility `json:"visibility"`
	CoverURL         *string    `json:"cover_url"`
	PublishedAt      time.Time  `json:"published_at"`
	AuthorName       string     `json:"author_name"`
	AuthorSlug       string     `json:"author_slug"`
	Origin           string     `json:"origin"`
	AvailableLocales []string   `json:"available_locales"`
	FallbackFrom     *string    `json:"fallback_from"`
}

type EditorListItem struct {
	ID           uuid.UUID     `json:"id"`
	Slug         string        `json:"slug"`
	CategoryKey  string        `json:"category_key"`
	SourceLocale string        `json:"source_locale"`
	Status       ArticleStatus `json:"status"`
	Visibility   Visibility    `json:"visibility"`
	Rev          int           `json:"rev"`
	PublishedRev int           `json:"published_rev"`
	Title        string        `json:"title"`
	Pending      bool          `json:"pending"` // a newer revision waits for approval
	UpdatedAt    time.Time     `json:"updated_at"`
}

type EditorArticle struct {
	Article   Article       `json:"article"`
	Source    *LocaleFile   `json:"source"`
	Revisions []RevisionRow `json:"revisions"`
	ViewerIDs []uuid.UUID   `json:"viewer_ids"`
}

// Request DTOs.
type CreateArticleReq struct {
	CategoryID   uuid.UUID `json:"category_id"`
	SourceLocale string    `json:"source_locale"`
	TitleForSlug string    `json:"title_for_slug"`
	Slug         string    `json:"slug"`
	CoverURL     *string   `json:"cover_url"`
}

type UpdateArticleReq struct {
	CategoryID *uuid.UUID `json:"category_id"`
	CoverURL   *string    `json:"cover_url"`
	ClearCover bool       `json:"clear_cover"`
}

type TextReq struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	BodyMD  string `json:"body_md"`
}
