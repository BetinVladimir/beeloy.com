// Package articles stores article texts in S3, keeps states/versions/search index in PostgreSQL and
// publishes only what a moderator approved.
package articles

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"beeloy.com/backend/internal/apperr"
	"beeloy.com/backend/internal/identity"
	"github.com/google/uuid"
)

const (
	MaxLocaleFileBytes = 512 * 1024
	MaxManifestBytes   = 64 * 1024
)

// Manifest is articles/{id}/manifest.json. It deliberately has NO publication status: that lives only in PostgreSQL.
type Manifest struct {
	SchemaVersion int        `json:"schema_version"`
	ArticleID     uuid.UUID  `json:"article_id"`
	Slug          string     `json:"slug"`
	CategoryKey   string     `json:"category_key"`
	SourceLocale  string     `json:"source_locale"`
	Rev           int        `json:"rev"`
	CoverURL      *string    `json:"cover_url"`
	AuthorID      *uuid.UUID `json:"author_id"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// LocaleFile is articles/{id}/rev-{rev}/{locale}.json.
type LocaleFile struct {
	SchemaVersion int       `json:"schema_version"`
	ArticleID     uuid.UUID `json:"article_id"`
	Locale        string    `json:"locale"`
	Rev           int       `json:"rev"`
	Origin        string    `json:"origin"` // human | machine
	Model         *string   `json:"model"`
	Title         string    `json:"title"`
	Summary       string    `json:"summary"`
	BodyMD        string    `json:"body_md"`
	GeneratedAt   time.Time `json:"generated_at"`
}

func ManifestKey(id uuid.UUID) string { return "articles/" + id.String() + "/manifest.json" }

func LocaleKey(id uuid.UUID, rev int, locale string) string {
	return fmt.Sprintf("articles/%s/rev-%d/%s.json", id, rev, locale)
}

var localeKeyRe = regexp.MustCompile(`^articles/([0-9a-f-]{36})/rev-([0-9]+)/([a-z]{2,3})\.json$`)

// ParseLocaleKey splits a locale file key.
func ParseLocaleKey(key string) (id uuid.UUID, rev int, locale string, ok bool) {
	m := localeKeyRe.FindStringSubmatch(key)
	if m == nil {
		return uuid.Nil, 0, "", false
	}
	id, err := uuid.Parse(m[1])
	if err != nil {
		return uuid.Nil, 0, "", false
	}
	rev, err = strconv.Atoi(m[2])
	if err != nil || rev < 1 {
		return uuid.Nil, 0, "", false
	}
	return id, rev, m[3], true
}

// ManifestKeyID extracts the article id from a manifest key.
func ManifestKeyID(key string) (uuid.UUID, bool) {
	m := regexp.MustCompile(`^articles/([0-9a-f-]{36})/manifest\.json$`).FindStringSubmatch(key)
	if m == nil {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(m[1])
	return id, err == nil
}

func fieldErr(f, c string) error { return apperr.Field(f, c) }

func ValidateManifest(m Manifest, keyID uuid.UUID, known func(string) bool) error {
	switch {
	case m.SchemaVersion != 1:
		return fieldErr("schema_version", "invalid_value")
	case m.ArticleID != keyID:
		return fieldErr("article_id", "invalid_value")
	case identity.Validate(identity.EntityArticle, m.Slug) != nil:
		return fieldErr("slug", "invalid_format")
	case !known(m.SourceLocale):
		return fieldErr("source_locale", "invalid_value")
	case m.Rev < 0:
		return fieldErr("rev", "invalid_value")
	case m.CategoryKey == "":
		return fieldErr("category_key", "required")
	}
	return nil
}

func checkLen(field, v string, min, max int) error {
	n := utf8.RuneCountInString(v)
	if n < min {
		if n == 0 {
			return fieldErr(field, "required")
		}
		return fieldErr(field, "too_short")
	}
	if n > max {
		return fieldErr(field, "too_long")
	}
	return nil
}

// ValidateTexts checks title/summary/body lengths (shared by API input and file validation).
func ValidateTexts(title, summary, body string) error {
	fields := map[string]string{}
	if err := checkLen("title", title, 3, 200); err != nil {
		fields["title"] = err.(*apperr.Error).Fields["title"]
	}
	if err := checkLen("summary", summary, 10, 400); err != nil {
		fields["summary"] = err.(*apperr.Error).Fields["summary"]
	}
	if err := checkLen("body_md", body, 1, 100000); err != nil {
		fields["body_md"] = err.(*apperr.Error).Fields["body_md"]
	}
	if len(fields) > 0 {
		return apperr.Validation(fields)
	}
	return nil
}

// ValidateLocaleFile validates a decoded locale file against the path it was found at.
func ValidateLocaleFile(f LocaleFile, raw []byte, wantID uuid.UUID, wantLocale string, wantRev int) error {
	switch {
	case len(raw) > MaxLocaleFileBytes:
		return fieldErr("file", "too_long")
	case f.SchemaVersion != 1:
		return fieldErr("schema_version", "invalid_value")
	case f.ArticleID != wantID:
		return fieldErr("article_id", "invalid_value")
	case f.Locale != wantLocale:
		return fieldErr("locale", "invalid_value")
	case f.Rev != wantRev:
		return fieldErr("rev", "invalid_value")
	case f.Origin != "human" && f.Origin != "machine":
		return fieldErr("origin", "invalid_value")
	}
	return ValidateTexts(f.Title, f.Summary, f.BodyMD)
}

// SearchText is what goes to to_tsvector (the body itself is never stored in PostgreSQL).
func SearchText(f LocaleFile) string { return f.Title + " " + f.Summary + " " + f.BodyMD }
