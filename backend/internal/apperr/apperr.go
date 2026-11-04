// Package apperr defines the application error type and the stable error codes of the HTTP API.
package apperr

import "net/http"

// Error is an API error. Err (if any) is never sent to the client.
type Error struct {
	Code    string
	HTTP    int
	Message string
	Fields  map[string]string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Message + ": " + e.Err.Error()
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// Wrap returns a copy of e that carries cause (for logs only).
func (e *Error) Wrap(cause error) *Error {
	c := *e
	c.Err = cause
	return &c
}

// WithMessage returns a copy of e with another message.
func (e *Error) WithMessage(msg string) *Error {
	c := *e
	c.Message = msg
	return &c
}

func New(code string, httpStatus int, msg string) *Error {
	return &Error{Code: code, HTTP: httpStatus, Message: msg}
}

// Validation builds a 422 error with per-field machine codes (required, too_short, too_long, invalid_format, invalid_value, taken, reserved).
func Validation(fields map[string]string) *Error {
	return &Error{Code: "validation_failed", HTTP: http.StatusUnprocessableEntity, Message: "validation failed", Fields: fields}
}

// Field is a shortcut for a validation error on a single field.
func Field(name, code string) *Error { return Validation(map[string]string{name: code}) }

var (
	ErrBadRequest      = New("bad_request", http.StatusBadRequest, "bad request")
	ErrInvalidCode     = New("invalid_code", http.StatusBadRequest, "invalid or expired code")
	ErrUnauthorized    = New("unauthorized", http.StatusUnauthorized, "authentication required")
	ErrForbidden       = New("forbidden", http.StatusForbidden, "forbidden")
	ErrNotFound        = New("not_found", http.StatusNotFound, "not found")
	ErrConflict        = New("conflict", http.StatusConflict, "conflict")
	ErrRateLimited     = New("rate_limited", http.StatusTooManyRequests, "too many requests")
	ErrPayloadTooLarge = New("payload_too_large", http.StatusRequestEntityTooLarge, "payload too large")
	ErrUnavailable     = New("unavailable", http.StatusServiceUnavailable, "service unavailable")
	ErrInternal        = New("internal", http.StatusInternalServerError, "internal error")
)

// Conflict returns a 409 with a specific machine reason in the message (e.g. "slug_taken").
func Conflict(reason string) *Error { return New("conflict", http.StatusConflict, reason) }

// Forbidden returns a 403 with a specific reason in the message (e.g. "blocked").
func Forbidden(reason string) *Error { return New("forbidden", http.StatusForbidden, reason) }

// RateLimited returns a 429; RetryAfter is carried in Fields["retry_after"] by the HTTP layer via RetryAfterSeconds.
func RateLimited(reason string, retryAfterSeconds int) *Error {
	e := New("rate_limited", http.StatusTooManyRequests, reason)
	e.Fields = map[string]string{"retry_after": itoa(retryAfterSeconds)}
	return e
}

// RetryAfterSeconds extracts the value stored by RateLimited (0 if absent).
func RetryAfterSeconds(e *Error) int {
	if e == nil || e.Fields == nil {
		return 0
	}
	n := 0
	for _, c := range e.Fields["retry_after"] {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
