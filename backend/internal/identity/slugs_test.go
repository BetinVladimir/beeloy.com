package identity_test

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/platform/ids"
	"beeloy.com/backend/internal/testutil"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Иван Петров":      "ivan-petrov",
		"Business Expert":  "business-expert",
		"  Ünïcode__Name ": "unicode-name",
		"Щука":             "shtuka",
		"!!!":              "",
		"a--b":             "a-b",
		"Straße":           "strasse",
	}
	for in, want := range cases {
		if got := identity.Slugify(in); got != want {
			t.Errorf("Slugify(%q)=%q want %q", in, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	type c struct {
		t    identity.EntityType
		s    string
		want string // "" = ok
	}
	cases := []c{
		{identity.EntityUser, "alice", ""}, {identity.EntityUser, "Alice", ""}, {identity.EntityUser, "ab", "too_short"},
		{identity.EntityUser, strings.Repeat("a", 33), "too_long"}, {identity.EntityUser, strings.Repeat("a", 32), ""},
		{identity.EntityUser, "admin", "reserved"}, {identity.EntityUser, "beeloy", "reserved"}, {identity.EntityUser, "people", "reserved"},
		{identity.EntityUser, "a_b", "invalid_format"}, {identity.EntityUser, "-ab", "invalid_format"}, {identity.EntityUser, "a--b", "invalid_format"},
		{identity.EntityUser, "иван", "invalid_format"}, {identity.EntityUser, "", "required"},
		{identity.EntityIdea, strings.Repeat("a", 80), ""}, {identity.EntityIdea, strings.Repeat("a", 81), "too_long"}, {identity.EntityIdea, "admin", ""},
		{identity.EntityArticle, "ab", "too_short"},
	}
	for _, c := range cases {
		err := identity.Validate(c.t, c.s)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s %q: unexpected %v", c.t, c.s, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "validation") {
			t.Errorf("%s %q: want %s got %v", c.t, c.s, c.want, err)
		}
	}
}

func TestSlugServiceFlow(t *testing.T) {
	pool := testutil.PG(t)
	ctx := context.Background()
	svc := &identity.SlugService{Pool: pool}
	a, b := testutil.InsertUser(t, pool, "a"), testutil.InsertUser(t, pool, "b")

	s1, err := svc.GenerateUnique(ctx, pool, identity.EntityUser, "Иван Петров")
	if err != nil || s1 != "ivan-petrov" {
		t.Fatalf("%q %v", s1, err)
	}
	if err := svc.Assign(ctx, pool, identity.EntityUser, a, s1); err != nil {
		t.Fatal(err)
	}
	s2, _ := svc.GenerateUnique(ctx, pool, identity.EntityUser, "Иван Петров")
	if s2 != "ivan-petrov-2" {
		t.Fatalf("collision suffix: %q", s2)
	}
	if err := svc.Assign(ctx, pool, identity.EntityUser, b, s1); err == nil {
		t.Fatal("slug of another entity must conflict")
	}
	if g, _ := svc.GenerateUnique(ctx, pool, identity.EntityUser, "!!"); g != "member" {
		t.Fatalf("fallback base: %q", g)
	}
	if g, _ := svc.GenerateUnique(ctx, pool, identity.EntityUser, "admin"); g == "admin" {
		t.Fatal("reserved must never be generated")
	}
	// first change is free; old slug stays reserved for the owner and resolves to the canonical one
	if err := svc.ChangeUserSlug(ctx, a, netip.Addr{}, "ivan"); err != nil {
		t.Fatal(err)
	}
	id, canon, err := svc.Resolve(ctx, pool, identity.EntityUser, "IVAN-PETROV")
	if err != nil || id != a || canon != "ivan" {
		t.Fatalf("resolve old slug: %v %v %v", id, canon, err)
	}
	if ok, _ := svc.IsAvailable(ctx, pool, identity.EntityUser, "ivan-petrov"); ok {
		t.Fatal("historical slug must not be available to others")
	}
	if err := svc.Assign(ctx, pool, identity.EntityUser, b, "ivan-petrov"); err == nil {
		t.Fatal("others cannot take the old slug")
	}
	// second change within 30 days is refused
	if err := svc.ChangeUserSlug(ctx, a, netip.Addr{}, "ivan2"); err == nil || !strings.Contains(err.Error(), "slug_change_too_soon") {
		t.Fatalf("want too_soon: %v", err)
	}
	// ...unless the current slug is older than 30 days
	if _, err := pool.Exec(ctx, `UPDATE slugs SET created_at = now() - interval '31 days' WHERE entity_id=$1 AND is_current`, a); err != nil {
		t.Fatal(err)
	}
	if err := svc.ChangeUserSlug(ctx, a, netip.Addr{}, "ivan-petrov"); err != nil { // owner may return to own old slug
		t.Fatal(err)
	}
	if cur, _ := svc.Current(ctx, pool, identity.EntityUser, a); cur != "ivan-petrov" {
		t.Fatalf("current: %q", cur)
	}
	// concurrent assignment of one slug: exactly one wins
	x, y := testutil.InsertUser(t, pool, "x"), testutil.InsertUser(t, pool, "y")
	res := make(chan error, 2)
	for _, id := range []interface{ String() string }{x, y} {
		uid := ids.New()
		_ = uid
		go func(id string) {
			u, _ := ids.Parse(id)
			res <- svc.Assign(ctx, pool, identity.EntityUser, u, "race-slug")
		}(id.String())
	}
	e1, e2 := <-res, <-res
	if (e1 == nil) == (e2 == nil) {
		t.Fatalf("exactly one must win: %v / %v", e1, e2)
	}
}
