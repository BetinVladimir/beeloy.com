// Package contentsync synchronizes S3 content into PostgreSQL: UI dictionaries (part A) and articles (part B).
package contentsync

import "time"

// Manifest is i18n/manifest.json (see docs/tz/06-content-sync.md).
type Manifest struct {
	SchemaVersion   int            `json:"schema_version"`
	Version         string         `json:"version"`
	PublishedAt     time.Time      `json:"published_at"`
	ReferenceLocale string         `json:"reference_locale"`
	Locales         []ManifestItem `json:"locales"`
}

type ManifestItem struct {
	Code   string `json:"code"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Bundle struct {
	Locale   string
	Raw      []byte
	Messages map[string]string
	SHA256   string
}

type Result string

const (
	ResultOK     Result = "ok"
	ResultNoop   Result = "noop"
	ResultFailed Result = "failed"
)

const (
	LockUI       int64 = 7100
	LockArticles int64 = 7101

	MaxFileSize  = 1 << 20  // 1 MiB per dictionary
	MaxTotalSize = 20 << 20 // 20 MiB per release
	maxProblems  = 50
)
