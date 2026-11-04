package search_test

import (
	"os"
	"testing"

	"beeloy.com/backend/internal/server/servertest"
	"beeloy.com/backend/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

func TestPeopleSearch(t *testing.T) {
	env := servertest.New(t)
	env.SignInSlug("a@example.com", "business-expert", "Business Expert")
	env.SignInSlug("b@example.com", "my-business-blog", "Jane Roe")
	env.SignInSlug("c@example.com", "janedoe", "Jane Doe")
	hidden := env.SignInSlug("d@example.com", "hidden-business", "Hidden Person")
	hidden.Patch("/api/v1/me", map[string]any{"profile_public": false})
	blocked := env.SignInSlug("e@example.com", "blocked-business", "Blocked Person")
	env.Server.Pool.Exec(t.Context(), `UPDATE users SET status='blocked' WHERE id=$1`, blocked.UserID())

	c := env.Client()
	slugs := func(q string) []string {
		r := c.Get("/api/v1/search/people?q=" + q)
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", q, r.Code, r.Raw)
		}
		var out []string
		for _, x := range r.List() {
			out = append(out, x.(map[string]any)["slug"].(string))
		}
		return out
	}
	got := slugs("business")
	if len(got) != 2 || got[0] != "business-expert" { // prefix match first, then substring
		t.Fatalf("business: %v", got)
	}
	if g := slugs("%40business-EXPERT"); len(g) != 1 || g[0] != "business-expert" {
		t.Fatalf("@-prefixed, case-insensitive: %v", g)
	}
	if g := slugs("jane"); len(g) != 2 {
		t.Fatalf("by nickname: %v", g)
	}
	if g := slugs("hidden"); len(g) != 0 {
		t.Fatalf("hidden profiles are not searchable: %v", g)
	}
	if g := slugs("blocked"); len(g) != 0 {
		t.Fatalf("blocked users are not searchable: %v", g)
	}
	if g := slugs("100%25"); len(g) != 0 { // LIKE metacharacters are escaped
		t.Fatalf("metacharacters: %v", g)
	}
	if r := c.Get("/api/v1/search/people?q=b"); r.Code != 422 {
		t.Errorf("short query: %d", r.Code)
	}
	r := c.Get("/api/v1/search/people?q=business")
	if _, leaked := r.List()[0].(map[string]any)["email"]; leaked {
		t.Error("no private data in results")
	}
}

func TestSitemapEntries(t *testing.T) {
	env := servertest.New(t)
	env.SignInSlug("a@example.com", "visible-person", "V")
	h := env.SignInSlug("b@example.com", "private-person", "P")
	h.Patch("/api/v1/me", map[string]any{"profile_public": false})
	c := env.Client()
	var slugs []string
	for _, x := range c.Get("/api/v1/seo/sitemap-entries?type=users").List() {
		slugs = append(slugs, x.(map[string]any)["path_slug"].(string))
	}
	if len(slugs) != 1 || slugs[0] != "visible-person" {
		t.Fatalf("users sitemap: %v", slugs)
	}
	if c.Get("/api/v1/seo/sitemap-entries?type=ideas").Code != 200 || c.Get("/api/v1/seo/sitemap-entries?type=articles").Code != 200 {
		t.Error("ideas/articles")
	}
	if c.Get("/api/v1/seo/sitemap-entries?type=zzz").Code != 400 || c.Get("/api/v1/seo/sitemap-entries?type=users&limit=501").Code != 400 {
		t.Error("bad params")
	}
}
