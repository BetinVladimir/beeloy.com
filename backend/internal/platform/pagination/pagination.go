// Package pagination implements opaque keyset cursors and limit parsing.
package pagination

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	"beeloy.com/backend/internal/apperr"
	"github.com/google/uuid"
)

const (
	DefaultLimit = 20
	MaxLimit     = 50
)

// Cursor marks the last item of a page: (T, ID) for (created_at, id) ordering; V is an optional secondary integer sort key.
type Cursor struct {
	T  time.Time `json:"t"`
	ID uuid.UUID `json:"id"`
	V  *int64    `json:"v,omitempty"`
}

func (c Cursor) IsZero() bool { return c.T.IsZero() && c.ID == uuid.Nil }

func Encode(c Cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Decode parses a cursor; an empty string yields a zero Cursor.
func Decode(s string) (Cursor, error) {
	var c Cursor
	if s == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, apperr.ErrBadRequest.WithMessage("bad cursor")
	}
	if err := json.Unmarshal(b, &c); err != nil || c.IsZero() {
		return Cursor{}, apperr.ErrBadRequest.WithMessage("bad cursor")
	}
	return c, nil
}

// ParseLimit reads ?limit= (default 20, 1..50; anything else is a bad request).
func ParseLimit(q url.Values) (int, error) { return ParseLimitMax(q, DefaultLimit, MaxLimit) }

func ParseLimitMax(q url.Values, def, max int) (int, error) {
	v := q.Get("limit")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		return 0, apperr.ErrBadRequest.WithMessage("bad limit")
	}
	return n, nil
}

// OffsetEncode/OffsetDecode implement the offset cursor used by search (max offset enforced by caller).
func OffsetEncode(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func OffsetDecode(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, apperr.ErrBadRequest.WithMessage("bad cursor")
	}
	n, err := strconv.Atoi(string(b))
	if err != nil || n < 0 {
		return 0, apperr.ErrBadRequest.WithMessage("bad cursor")
	}
	return n, nil
}

// Page trims items fetched with limit+1 and tells whether another page exists.
func Page[T any](items []T, limit int) (page []T, more bool) {
	if len(items) > limit {
		return items[:limit], true
	}
	return items, false
}
