package ideas_test

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/ideas"
	"beeloy.com/backend/internal/server/servertest"
	"beeloy.com/backend/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

type world struct {
	env                      *servertest.Env
	author, other, mod, anon *servertest.Client
}

func newWorld(t *testing.T) *world {
	env := servertest.New(t)
	w := &world{env: env, anon: env.Client(), author: env.LoginAs("author@example.com"), other: env.LoginAs("other@example.com"), mod: env.LoginAs("mod@example.com")}
	env.Grant(w.mod.UserID(), acl.RoleModerator, nil)
	return w
}

func (w *world) submit(t *testing.T, c *servertest.Client, title string) map[string]any {
	r := c.Post("/api/v1/ideas", map[string]any{"title": title, "body": "A detailed description of the idea that is long enough.", "category": "product", "locale": "bg"})
	if r.Code != 201 {
		t.Fatalf("submit: %d %s", r.Code, r.Raw)
	}
	return r.Data()
}

func (w *world) status(c *servertest.Client, id, to, note string, extra map[string]any) *servertest.Resp {
	body := map[string]any{"to": to, "note": note}
	for k, v := range extra {
		body[k] = v
	}
	return c.Post("/api/v1/ideas/"+id+"/status", body)
}

func TestSubmitAndPublicRead(t *testing.T) {
	w := newWorld(t)
	idea := w.submit(t, w.author, "Better onboarding for new members")
	if idea["status"] != "submitted" || idea["slug"] != "better-onboarding-for-new-members" || idea["votes_count"].(float64) != 0 {
		t.Fatalf("idea: %v", idea)
	}
	a := idea["author"].(map[string]any)
	if a["nickname"] != "author" || a["slug"] != "author" {
		t.Fatalf("author must be shown with nickname and slug: %v", a)
	}
	pub := w.anon.Get("/api/v1/ideas/better-onboarding-for-new-members")
	if pub.Code != 200 || pub.Header.Get("Cache-Control") != "private, no-store" || len(pub.Data()["history"].([]any)) != 1 {
		t.Fatalf("public read: %d %s", pub.Code, pub.Raw)
	}
	if lst := w.anon.Get("/api/v1/ideas").List(); len(lst) != 1 {
		t.Fatalf("list: %d", len(lst))
	}
	// validation and permissions
	if r := w.author.Post("/api/v1/ideas", map[string]any{"title": "no", "body": "short", "category": "x", "locale": "zz"}); r.Code != 422 {
		t.Errorf("validation: %d", r.Code)
	}
	if r := w.anon.Post("/api/v1/ideas", map[string]any{"title": "Anonymous idea", "body": strings.Repeat("x", 30), "category": "product", "locale": "bg"}); r.Code != 401 {
		t.Errorf("anonymous submit: %d", r.Code)
	}
	if r := w.author.Post("/api/v1/ideas", map[string]any{"title": "Extra", "body": strings.Repeat("x", 30), "category": "product", "locale": "bg", "author_id": "x"}); r.Code != 400 {
		t.Errorf("author_id in body must be rejected: %d", r.Code)
	}
}

func TestAllTransitions(t *testing.T) {
	all := []ideas.Status{ideas.Submitted, ideas.UnderReview, ideas.NeedsInfo, ideas.Planned, ideas.InProgress, ideas.Delivered, ideas.Declined, ideas.Duplicate}
	allowed := map[ideas.Status][]ideas.Status{
		ideas.Submitted:   {ideas.UnderReview, ideas.NeedsInfo, ideas.Declined, ideas.Duplicate},
		ideas.NeedsInfo:   {ideas.UnderReview, ideas.Declined},
		ideas.UnderReview: {ideas.Planned, ideas.NeedsInfo, ideas.Declined, ideas.Duplicate},
		ideas.Planned:     {ideas.InProgress, ideas.Declined},
		ideas.InProgress:  {ideas.Delivered, ideas.Planned},
	}
	in := func(l []ideas.Status, s ideas.Status) bool {
		for _, x := range l {
			if x == s {
				return true
			}
		}
		return false
	}
	for _, from := range all {
		for _, to := range all {
			if want := in(allowed[from], to); ideas.CanTransition(from, to) != want {
				t.Errorf("%s -> %s: want %v", from, to, want)
			}
		}
	}
}

func TestStatusFlowThroughApi(t *testing.T) {
	w := newWorld(t)
	idea := w.submit(t, w.author, "Dark mode for the whole site")
	id := idea["id"].(string)
	main := w.submit(t, w.other, "Dark theme everywhere please")["id"].(string)

	if r := w.status(w.author, id, "under_review", "", nil); r.Code != 403 {
		t.Errorf("author cannot set status: %d", r.Code)
	}
	if r := w.status(w.anon, id, "under_review", "", nil); r.Code != 401 {
		t.Errorf("anon: %d", r.Code)
	}
	if r := w.status(w.mod, id, "delivered", "", nil); r.Code != 409 {
		t.Errorf("submitted -> delivered: %d", r.Code)
	}
	if r := w.status(w.mod, id, "declined", "short", nil); r.Code != 422 {
		t.Errorf("declined needs a note >= 10: %d", r.Code)
	}
	if r := w.status(w.mod, id, "duplicate", "Same as another idea", nil); r.Code != 422 {
		t.Errorf("duplicate needs duplicate_of: %d", r.Code)
	}
	if r := w.status(w.mod, id, "duplicate", "Same as another idea", map[string]any{"duplicate_of_id": id}); r.Code != 422 {
		t.Errorf("duplicate of itself: %d", r.Code)
	}
	if r := w.status(w.mod, id, "under_review", "", nil); r.Code != 200 || r.Data()["status"] != "under_review" {
		t.Fatalf("review: %d %s", r.Code, r.Raw)
	}
	if r := w.status(w.mod, id, "duplicate", "Same as the other idea.", map[string]any{"duplicate_of_id": main}); r.Code != 200 || r.Data()["duplicate_of_slug"] != "dark-theme-everywhere-please" {
		t.Fatalf("duplicate: %d %s", r.Code, r.Raw)
	}
	if r := w.status(w.mod, id, "planned", "", nil); r.Code != 409 {
		t.Errorf("terminal status: %d", r.Code)
	}
	// the other idea walks the happy path
	for _, to := range []string{"under_review", "planned", "in_progress", "delivered"} {
		if r := w.status(w.mod, main, to, "", nil); r.Code != 200 {
			t.Fatalf("-> %s: %d %s", to, r.Code, r.Raw)
		}
	}
	// reply
	if r := w.other.Put("/api/v1/ideas/"+main+"/reply", map[string]any{"text": "thanks"}); r.Code != 403 {
		t.Errorf("member reply: %d", r.Code)
	}
	if r := w.mod.Put("/api/v1/ideas/"+main+"/reply", map[string]any{"text": "Shipped in v2."}); r.Code != 200 || r.Data()["team_reply"] != "Shipped in v2." {
		t.Fatalf("reply: %d %s", r.Code, r.Raw)
	}
	g := w.anon.Get("/api/v1/ideas/dark-theme-everywhere-please")
	if h := g.Data()["history"].([]any); len(h) != 5 {
		t.Errorf("history entries: %d", len(h))
	}
	var n int
	w.env.Server.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='idea.status'`).Scan(&n)
	if n != 6 {
		t.Errorf("audit events: %d", n)
	}
}

func TestEditRules(t *testing.T) {
	w := newWorld(t)
	id := w.submit(t, w.author, "Editable idea title")["id"].(string)
	patch := map[string]any{"title": "Edited idea title", "body": "An edited and still long enough description."}
	if r := w.other.Patch("/api/v1/ideas/"+id, patch); r.Code != 403 {
		t.Errorf("non-author edit: %d", r.Code)
	}
	if r := w.author.Patch("/api/v1/ideas/"+id, patch); r.Code != 200 || r.Data()["title"] != "Edited idea title" {
		t.Fatalf("author edit: %d %s", r.Code, r.Raw)
	}
	w.status(w.mod, id, "needs_info", "", nil)
	if r := w.author.Patch("/api/v1/ideas/"+id, map[string]any{"body": "More details, as requested by the team."}); r.Code != 200 || r.Data()["status"] != "under_review" {
		t.Fatalf("edit in needs_info moves to under_review: %d %s", r.Code, r.Raw)
	}
	if r := w.author.Patch("/api/v1/ideas/"+id, patch); r.Code != 409 {
		t.Errorf("edit under review is not allowed: %d", r.Code)
	}
}

func TestVotes(t *testing.T) {
	w := newWorld(t)
	id := w.submit(t, w.author, "Votable idea")["id"].(string)
	if r := w.author.Put("/api/v1/ideas/"+id+"/vote", nil); r.Code != 409 {
		t.Errorf("own idea: %d", r.Code)
	}
	if r := w.anon.Put("/api/v1/ideas/"+id+"/vote", nil); r.Code != 401 {
		t.Errorf("anon vote: %d", r.Code)
	}
	// 20 parallel votes by one user count once
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := w.env.Client()
			c.Cookies = w.other.Cookies
			if r := c.Put("/api/v1/ideas/"+id+"/vote", nil); r.Code != 204 {
				t.Errorf("vote: %d", r.Code)
			}
		}()
	}
	wg.Wait()
	g := w.other.Get("/api/v1/ideas/votable-idea")
	if i := g.Data()["idea"].(map[string]any); i["votes_count"].(float64) != 1 || i["viewer_voted"] != true {
		t.Fatalf("votes: %v", i)
	}
	if w.anon.Get("/api/v1/ideas/votable-idea").Data()["idea"].(map[string]any)["viewer_voted"] != false {
		t.Error("viewer_voted is false for anonymous")
	}
	w.other.Delete("/api/v1/ideas/" + id + "/vote")
	w.other.Delete("/api/v1/ideas/" + id + "/vote") // idempotent
	if i := w.anon.Get("/api/v1/ideas/votable-idea").Data()["idea"].(map[string]any); i["votes_count"].(float64) != 0 {
		t.Errorf("after unvote: %v", i["votes_count"])
	}
	// closed ideas take no votes
	w.status(w.mod, id, "declined", "We decided against it.", nil)
	if r := w.other.Put("/api/v1/ideas/"+id+"/vote", nil); r.Code != 409 {
		t.Errorf("vote on declined: %d", r.Code)
	}
}

func TestDailyLimitAndListing(t *testing.T) {
	w := newWorld(t)
	for i := 0; i < 5; i++ {
		w.submit(t, w.author, fmt.Sprintf("Idea number %d", i))
	}
	r := w.author.Post("/api/v1/ideas", map[string]any{"title": "One too many", "body": strings.Repeat("y", 30), "category": "product", "locale": "bg"})
	if r.Code != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("6th idea: %d %s", r.Code, r.Raw)
	}
	// paging, sort=top and filters
	other := w.submit(t, w.other, "Popular other idea")["id"].(string)
	w.mod.Put("/api/v1/ideas/"+other+"/vote", nil)
	w.author.Put("/api/v1/ideas/"+other+"/vote", nil)
	top := w.anon.Get("/api/v1/ideas?sort=top&limit=2")
	if top.Code != 200 || len(top.List()) != 2 || top.List()[0].(map[string]any)["id"] != other || top.JSON["next_cursor"] == nil {
		t.Fatalf("top: %s", top.Raw)
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		u := "/api/v1/ideas?sort=top&limit=2"
		if cursor != "" {
			u += "&cursor=" + cursor
		}
		p := w.anon.Get(u)
		for _, it := range p.List() {
			id := it.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("duplicate across pages: %s", id)
			}
			seen[id] = true
		}
		nc, _ := p.JSON["next_cursor"].(string)
		if nc == "" {
			break
		}
		cursor = nc
	}
	if len(seen) != 6 {
		t.Errorf("all ideas must be reachable by paging: %d", len(seen))
	}
	if n := len(w.anon.Get("/api/v1/ideas?status=planned").List()); n != 0 {
		t.Errorf("status filter: %d", n)
	}
	if w.anon.Get("/api/v1/ideas?status=bogus").Code != 400 || w.anon.Get("/api/v1/ideas?sort=weird").Code != 400 {
		t.Error("bad filters")
	}
	if w.anon.Get("/api/v1/ideas?mine=1").Code != 401 {
		t.Error("mine requires auth")
	}
	if n := len(w.author.Get("/api/v1/ideas?mine=1").List()); n != 5 {
		t.Errorf("mine: %d", n)
	}
	// profile activity
	if n := len(w.anon.Get("/api/v1/users/author/ideas").List()); n != 5 {
		t.Errorf("profile ideas: %d", n)
	}
	w.author.Patch("/api/v1/me", map[string]any{"profile_public": false})
	if n := len(w.anon.Get("/api/v1/users/author/ideas").List()); n != 0 {
		t.Errorf("hidden activity: %d", n)
	}
	if w.anon.Get("/api/v1/users/nobody-x/ideas").Code != 404 {
		t.Error("unknown user")
	}
}
