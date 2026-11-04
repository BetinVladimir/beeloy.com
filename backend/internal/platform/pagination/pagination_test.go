package pagination

import (
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTrip(t *testing.T) {
	v := int64(7)
	c := Cursor{T: time.Date(2026, 11, 4, 10, 0, 0, 123456789, time.UTC), ID: uuid.Must(uuid.NewV7()), V: &v}
	d, err := Decode(Encode(c))
	if err != nil || !d.T.Equal(c.T) || d.ID != c.ID || d.V == nil || *d.V != 7 {
		t.Fatalf("round trip: %+v %v", d, err)
	}
	if z, err := Decode(""); err != nil || !z.IsZero() {
		t.Fatal("empty must decode to zero")
	}
	for _, bad := range []string{"%%%", "e30", "bm90LWpzb24"} {
		if _, err := Decode(bad); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
}

func TestLimit(t *testing.T) {
	cases := map[string]struct {
		n  int
		ok bool
	}{"": {20, true}, "1": {1, true}, "50": {50, true}, "0": {0, false}, "51": {0, false}, "x": {0, false}, "-1": {0, false}}
	for in, want := range cases {
		n, err := ParseLimit(url.Values{"limit": {in}})
		if in == "" {
			n, err = ParseLimit(url.Values{})
		}
		if (err == nil) != want.ok || (want.ok && n != want.n) {
			t.Errorf("limit %q => %d %v", in, n, err)
		}
	}
}

func TestOffsetAndPage(t *testing.T) {
	if n, err := OffsetDecode(OffsetEncode(40)); err != nil || n != 40 {
		t.Fatal("offset round trip")
	}
	if _, err := OffsetDecode("@@"); err == nil {
		t.Fatal("bad offset")
	}
	p, more := Page([]int{1, 2, 3}, 2)
	if len(p) != 2 || !more {
		t.Fatal("page more")
	}
	p, more = Page([]int{1, 2}, 2)
	if len(p) != 2 || more {
		t.Fatal("page last")
	}
}
