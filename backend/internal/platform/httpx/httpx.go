// Package httpx contains HTTP response helpers and the middleware chain of the API.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"

	"beeloy.com/backend/internal/apperr"
)

type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyClientIP
	keyIngressOK
)

func RequestIDFrom(ctx context.Context) string { s, _ := ctx.Value(keyRequestID).(string); return s }

// ClientIPFrom returns the real client IP resolved by RealIP (zero Addr if unknown).
func ClientIPFrom(ctx context.Context) netip.Addr {
	a, _ := ctx.Value(keyClientIP).(netip.Addr)
	return a
}

// ClientIP is a convenience wrapper for handlers.
func ClientIP(r *http.Request) netip.Addr { return ClientIPFrom(r.Context()) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// JSON writes {"data": data}.
func JSON(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, map[string]any{"data": data})
}

// JSONWith writes {"data": data, ...extra} (e.g. canonical_slug next to data).
func JSONWith(w http.ResponseWriter, status int, data any, extra map[string]any) {
	m := map[string]any{"data": data}
	for k, v := range extra {
		m[k] = v
	}
	writeJSON(w, status, m)
}

// JSONList writes {"data": [...], "next_cursor": next|null}.
func JSONList(w http.ResponseWriter, data any, next string) {
	var nc any
	if next != "" {
		nc = next
	}
	if data == nil {
		data = []any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "next_cursor": nc})
}

func NoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// WriteError converts any error into the API error format. Unknown errors become "internal" and are logged.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			ae = apperr.ErrPayloadTooLarge
		} else {
			ae = apperr.ErrInternal.Wrap(err)
		}
	}
	if ae.HTTP >= 500 {
		slog.Error("request failed", "request_id", RequestIDFrom(r.Context()), "path", r.URL.Path, "code", ae.Code, "err", ae.Err)
	}
	body := map[string]any{"code": ae.Code, "message": ae.Message}
	if ae.Code == "rate_limited" {
		if s := apperr.RetryAfterSeconds(ae); s > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(s))
		}
	} else if len(ae.Fields) > 0 {
		body["fields"] = ae.Fields
	}
	if ae.HTTP >= 500 {
		body["message"] = ae.Message // generic, never the cause
	}
	writeJSON(w, ae.HTTP, map[string]any{"error": body})
}

// DecodeJSON reads exactly one JSON object, rejecting unknown fields and trailing data.
func DecodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return apperr.ErrPayloadTooLarge
		}
		return apperr.ErrBadRequest.WithMessage("invalid JSON body")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return apperr.ErrBadRequest.WithMessage("invalid JSON body")
	}
	return nil
}
