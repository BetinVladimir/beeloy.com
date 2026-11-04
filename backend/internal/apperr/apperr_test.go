package apperr

import (
	"errors"
	"testing"
)

func TestErrors(t *testing.T) {
	cause := errors.New("db down")
	e := ErrInternal.Wrap(cause)
	if !errors.Is(e, cause) {
		t.Fatal("Unwrap must expose cause")
	}
	if ErrInternal.Err != nil {
		t.Fatal("Wrap must not mutate the shared error")
	}
	v := Field("title", "too_short")
	if v.HTTP != 422 || v.Fields["title"] != "too_short" {
		t.Fatalf("bad validation error: %+v", v)
	}
	r := RateLimited("slow down", 42)
	if r.HTTP != 429 || RetryAfterSeconds(r) != 42 {
		t.Fatalf("bad rate limited: %+v", r)
	}
	if Conflict("slug_taken").Message != "slug_taken" || Forbidden("blocked").HTTP != 403 {
		t.Fatal("bad helpers")
	}
}
