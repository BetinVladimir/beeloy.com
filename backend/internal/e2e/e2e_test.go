// Package e2e holds cross-cutting negative security tests that exercise the whole HTTP stack.
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/articles"
	"beeloy.com/backend/internal/server/servertest"
	"beeloy.com/backend/internal/testutil"
	"github.com/google/uuid"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

type world struct {
	env                                       *servertest.Env
	alice, bob, eve, mod, admin, editor, anon *servertest.Client
}

func newWorld(t *testing.T) *world {
	env := servertest.New(t)
	w := &world{env: env, anon: env.Client()}
	w.alice, w.bob, w.eve = env.LoginAs("alice@example.com"), env.LoginAs("bob@example.com"), env.LoginAs("eve@example.com")
	w.mod, w.admin, w.editor = env.LoginAs("mod@example.com"), env.LoginAs("admin@example.com"), env.LoginAs("editor@example.com")
	env.Grant(w.mod.UserID(), acl.RoleModerator, nil)
	env.Grant(w.admin.UserID(), acl.RoleAdmin, nil)
	env.Grant(w.editor.UserID(), acl.RoleEditor, nil)
	env.Server.Pool.Exec(context.Background(), `UPDATE users SET created_at = now() - interval '3 days'`)
	return w
}

func send(c *servertest.Client, conv, body string) *servertest.Resp {
	return c.Post("/api/v1/conversations/"+conv+"/messages", map[string]any{"client_id": uuid.NewString(), "body": body})
}

// 1 + 13: strangers and staff never read private conversations.
func TestPrivateConversationsAreIsolated(t *testing.T) {
	w := newWorld(t)
	conv := w.alice.Post("/api/v1/conversations", map[string]any{"user_id": w.bob.UserID()}).Data()["id"].(string)
	send(w.alice, conv, "alice to bob, top secret")
	for name, c := range map[string]*servertest.Client{"eve": w.eve, "moderator": w.mod, "admin": w.admin} {
		if r := c.Get("/api/v1/conversations/" + conv + "/messages"); r.Code != 404 {
			t.Errorf("%s reads messages: %d", name, r.Code)
		}
		if r := send(c, conv, "intrusion"); r.Code != 404 {
			t.Errorf("%s sends: %d", name, r.Code)
		}
		if strings.Contains(string(c.Get("/api/v1/conversations?folder=requests").Raw), "top secret") {
			t.Errorf("%s sees text in lists", name)
		}
	}
	exp := w.eve.Get("/api/v1/me/export")
	if strings.Contains(string(exp.Raw), "top secret") {
		t.Error("export of another user leaks messages")
	}
	if r, _ := http.Get(fmt.Sprintf("%s/api/v1/events", w.env.HTTPServer().URL)); r.StatusCode != 401 {
		t.Errorf("anonymous SSE: %d", r.StatusCode)
	}
	// staff reach private text only through a report snapshot, and every view is audited
	w.bob.Post("/api/v1/conversations/"+conv+"/accept", nil)
	m := send(w.alice, conv, "this one is abusive").Data()["id"].(string)
	rep := w.bob.Post("/api/v1/reports", map[string]any{"target_type": "message", "target_id": m, "reason": "abuse"})
	if rep.Code != 201 {
		t.Fatalf("report: %d %s", rep.Code, rep.Raw)
	}
	w.mod.Get("/api/v1/mod/reports/" + rep.Data()["id"].(string))
	var n int
	w.env.Server.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='report.view_snapshot'`).Scan(&n)
	if n != 1 {
		t.Errorf("snapshot view audit: %d", n)
	}
}

// 2: privileged endpoints refuse plain members and anonymous callers.
func TestPrivilegedEndpointsRefuseMembers(t *testing.T) {
	w := newWorld(t)
	id := uuid.NewString()
	type call struct{ method, path string }
	calls := []call{
		{"GET", "/api/v1/mod/reports"}, {"GET", "/api/v1/mod/articles"}, {"GET", "/api/v1/admin/users"}, {"GET", "/api/v1/admin/audit"},
		{"GET", "/api/v1/editor/articles"}, {"POST", "/api/v1/mod/users/" + id + "/block"}, {"POST", "/api/v1/mod/articles/" + id + "/publish"},
		{"POST", "/api/v1/admin/role-bindings"}, {"POST", "/api/v1/mod/comments/" + id + "/hide"}, {"POST", "/api/v1/ideas/" + id + "/status"},
		{"PUT", "/api/v1/ideas/" + id + "/reply"}, {"DELETE", "/api/v1/admin/role-bindings/" + id}, {"POST", "/api/v1/mod/reports/" + id + "/resolve"},
	}
	for _, c := range calls {
		if r := w.eve.Do(c.method, c.path, map[string]any{}); r.Code != 403 && r.Code != 404 {
			t.Errorf("member %s %s: %d", c.method, c.path, r.Code)
		}
		if r := w.anon.Do(c.method, c.path, map[string]any{}); r.Code != 401 {
			t.Errorf("anonymous %s %s: %d", c.method, c.path, r.Code)
		}
	}
}

// 3: state-changing requests need Origin and X-Requested-With.
func TestCSRFProtection(t *testing.T) {
	w := newWorld(t)
	srv := w.env.HTTPServer()
	do := func(origin, xrw string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/auth/logout", nil)
		req.AddCookie(&http.Cookie{Name: "bl_session", Value: w.eve.Cookie()})
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if xrw != "" {
			req.Header.Set("X-Requested-With", xrw)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	if do("", "") != 403 || do("https://evil.example", "beeloy") != 403 || do("https://beeloy.com", "") != 403 {
		t.Error("forged requests must be refused")
	}
	if w.eve.Get("/api/v1/me").Code != 200 {
		t.Error("the forged logout must not have worked")
	}
	if do("https://beeloy.com", "beeloy") != 204 {
		t.Error("a legitimate request passes")
	}
}

// 4: ownership is decided by the server, never by the request body.
func TestClientCannotSpoofOwnership(t *testing.T) {
	w := newWorld(t)
	body := map[string]any{"title": "Spoofed idea", "body": strings.Repeat("spoof ", 10), "category": "other", "locale": "en"}
	spoof := map[string]any{"author_id": w.bob.UserID()}
	for k, v := range body {
		spoof[k] = v
	}
	if r := w.eve.Post("/api/v1/ideas", spoof); r.Code != 400 {
		t.Errorf("unknown field author_id: %d", r.Code)
	}
	idea := w.eve.Post("/api/v1/ideas", body)
	if idea.Data()["author"].(map[string]any)["user_id"] != w.eve.UserID() {
		t.Error("author must be the signed-in user")
	}
	if r := w.bob.Patch("/api/v1/ideas/"+idea.Data()["id"].(string), map[string]any{"title": "Hijacked title"}); r.Code != 403 {
		t.Errorf("editing someone else's idea: %d", r.Code)
	}
	if r := w.eve.Patch("/api/v1/me", map[string]any{"status": "active", "id": w.bob.UserID()}); r.Code != 400 {
		t.Errorf("profile patch with extra fields: %d", r.Code)
	}
	cm := w.eve.Post("/api/v1/comments", map[string]any{"target_type": "idea", "target_id": idea.Data()["id"], "body": "my comment"}).Data()["id"].(string)
	if r := w.bob.Delete("/api/v1/comments/" + cm); r.Code != 403 {
		t.Errorf("deleting someone else's comment: %d", r.Code)
	}
}

// 5: the one-time code cannot be brute-forced.
func TestOTPBruteForce(t *testing.T) {
	w := newWorld(t)
	c := w.env.Client()
	c.Post("/api/v1/auth/otp/request", map[string]any{"email": "victim@example.com"})
	re := regexp.MustCompile(`\b(\d{6})\b`)
	msgs := w.env.Mailer.Messages()
	real := re.FindString(msgs[len(msgs)-1].TextBody)
	hit := 0
	for i := 0; i < 20; i++ {
		guess := fmt.Sprintf("%06d", i)
		if guess == real {
			guess = "999999"
		}
		c.WithIP(fmt.Sprintf("203.0.113.%d", i+1))
		if r := c.Post("/api/v1/auth/otp/verify", map[string]any{"email": "victim@example.com", "code": guess, "accept_terms": true}); r.Code == 200 {
			hit++
		}
	}
	if hit != 0 {
		t.Fatal("a wrong code must never log in")
	}
	c.WithIP("203.0.113.200")
	if r := c.Post("/api/v1/auth/otp/verify", map[string]any{"email": "victim@example.com", "code": real, "accept_terms": true}); r.Code == 200 {
		t.Fatal("after 5 failed attempts even the real code is dead")
	}
	// and per-IP verification attempts are limited as well
	codes := map[int]int{}
	one := w.env.Client().WithIP("198.51.100.99")
	for i := 0; i < 14; i++ {
		codes[one.Post("/api/v1/auth/otp/verify", map[string]any{"email": fmt.Sprintf("x%d@example.com", i), "code": "000000", "accept_terms": true}).Code]++
	}
	if codes[429] == 0 {
		t.Errorf("per-IP verify limit missing: %v", codes)
	}
}

// 6 + 7: role revocation and blocking take effect on the next request.
func TestRevocationIsImmediate(t *testing.T) {
	w := newWorld(t)
	if w.mod.Get("/api/v1/mod/reports").Code != 200 {
		t.Fatal("precondition")
	}
	w.env.Server.Pool.Exec(context.Background(), `DELETE FROM role_bindings WHERE user_id=$1`, w.mod.UserID())
	if w.mod.Get("/api/v1/mod/reports").Code != 403 {
		t.Error("revoked role must stop working at once")
	}
	if w.eve.Get("/api/v1/me").Code != 200 {
		t.Fatal("precondition")
	}
	w.admin.Post("/api/v1/admin/role-bindings", map[string]any{"user_id": w.mod.UserID(), "role": "moderator"})
	w.mod.Post("/api/v1/mod/users/"+w.eve.UserID()+"/block", map[string]any{"reason": "testing the block"})
	if w.eve.Get("/api/v1/me").Code != 401 {
		t.Error("blocked user's old session must be dead")
	}
	if r := w.env.Client().Get("/api/v1/users/" + "eve"); r.Code == 200 {
		t.Error("blocked profile must be hidden")
	}
}

var leakRe = regexp.MustCompile(`(?i)token_hash|"subject"|@example\.com|"email"|password|block_reason|"ip"`)

// 8: public responses never contain private fields.
func TestPublicResponsesLeakNothing(t *testing.T) {
	w := newWorld(t)
	idea := w.alice.Post("/api/v1/ideas", map[string]any{"title": "Public idea here", "body": strings.Repeat("public ", 10), "category": "other", "locale": "en"}).Data()
	w.bob.Post("/api/v1/comments", map[string]any{"target_type": "idea", "target_id": idea["id"], "body": "nice"})
	slugA := w.alice.Get("/api/v1/me").Data()["slug"].(string)
	for _, p := range []string{"/api/v1/users/" + slugA, "/api/v1/ideas", "/api/v1/ideas/" + idea["slug"].(string), "/api/v1/comments?target_type=idea&target_id=" + idea["id"].(string),
		"/api/v1/search/people?q=" + slugA, "/api/v1/seo/sitemap-entries?type=users", "/api/v1/articles?locale=en", "/api/v1/categories", "/api/v1/i18n/meta", "/api/v1/resources/" + idea["id"].(string)} {
		r := w.anon.Get(p)
		if r.Code != 200 {
			t.Errorf("%s: %d", p, r.Code)
			continue
		}
		if m := leakRe.FindString(string(r.Raw)); m != "" {
			t.Errorf("%s leaks %q: %s", p, m, r.Raw)
		}
	}
	// the private view of the owner does contain the status but still not the e-mail
	if me := w.alice.Get("/api/v1/me"); strings.Contains(string(me.Raw), "@example.com") {
		t.Error("even /me never returns the e-mail")
	}
}

func putManifest(t *testing.T, env *servertest.Env, id uuid.UUID, slug string) {
	m := articles.Manifest{SchemaVersion: 1, ArticleID: id, Slug: slug, CategoryKey: "news", SourceLocale: "en", Rev: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	b, _ := json.Marshal(m)
	env.Store.Put(context.Background(), articles.ManifestKey(id), b, "application/json")
	f := articles.LocaleFile{SchemaVersion: 1, ArticleID: id, Locale: "en", Rev: 1, Origin: "human", Title: "Uploaded title", Summary: "Uploaded summary text.", BodyMD: "uploaded zebra body", GeneratedAt: time.Now()}
	b, _ = json.Marshal(f)
	env.Store.Put(context.Background(), articles.LocaleKey(id, 1, "en"), b, "application/json")
}

// 9 + 10 + 11 + 12: unpublished, uploaded, tampered and restricted articles.
func TestArticleVisibilityRules(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	// draft and in_review created through the editor
	d := w.editor.Post("/api/v1/editor/articles", map[string]any{"category_id": servertest.CatNews, "source_locale": "en", "title_for_slug": "Draft zebra"}).Data()["id"].(string)
	w.editor.Put("/api/v1/editor/articles/"+d+"/source", map[string]any{"title": "Draft zebra title", "summary": "A summary for the draft.", "body_md": "zebra in a draft"})
	r := w.editor.Post("/api/v1/editor/articles", map[string]any{"category_id": servertest.CatNews, "source_locale": "en", "title_for_slug": "Review zebra"}).Data()["id"].(string)
	w.editor.Put("/api/v1/editor/articles/"+r+"/source", map[string]any{"title": "Review zebra title", "summary": "A summary for review.", "body_md": "zebra in review"})
	w.editor.Post("/api/v1/editor/articles/"+r+"/submit", nil)
	// uploaded straight to S3, then synchronized
	up := uuid.New()
	putManifest(t, w.env, up, "uploaded-zebra")
	// (the sync itself is covered in contentsync tests; here only the visible effect matters)
	syncer := newSyncer(w.env)
	if _, _, err := syncer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"draft-zebra", "review-zebra", "uploaded-zebra"} {
		if code := w.anon.Get("/api/v1/articles/" + slug + "?locale=en").Code; code != 404 {
			t.Errorf("%s by slug: %d", slug, code)
		}
		if code := w.eve.Get("/api/v1/articles/" + slug + "?locale=en").Code; code != 404 {
			t.Errorf("%s for a member: %d", slug, code)
		}
	}
	if len(w.anon.Get("/api/v1/articles?locale=en").List()) != 0 || len(w.anon.Get("/api/v1/search/articles?q=zebra&locale=en").List()) != 0 ||
		len(w.anon.Get("/api/v1/seo/sitemap-entries?type=articles").List()) != 0 {
		t.Error("unpublished content must not be listed, searched or in the sitemap")
	}
	// 11: the editor cannot publish
	if code := w.editor.Post("/api/v1/mod/articles/"+r+"/publish", map[string]any{"rev": 1, "visibility": "public"}).Code; code != 403 {
		t.Errorf("editor publish: %d", code)
	}
	// 12: restricted
	w.mod.Post("/api/v1/mod/articles/"+r+"/publish", map[string]any{"rev": 1, "visibility": "restricted", "viewer_ids": []string{w.alice.UserID()}})
	if w.eve.Get("/api/v1/articles/review-zebra?locale=en").Code != 404 || w.anon.Get("/api/v1/articles/review-zebra?locale=en").Code != 404 {
		t.Error("outsiders must not see a restricted article")
	}
	if w.alice.Get("/api/v1/articles/review-zebra?locale=en").Code != 200 {
		t.Error("the assigned viewer can read it")
	}
	if len(w.anon.Get("/api/v1/search/articles?q=zebra&locale=en").List()) != 0 || len(w.anon.Get("/api/v1/seo/sitemap-entries?type=articles").List()) != 0 {
		t.Error("restricted articles are not searchable and not in the sitemap")
	}
	// 10: a replaced file in the bucket is not served
	w.mod.Post("/api/v1/mod/articles/"+up.String()+"/publish", map[string]any{"rev": 1, "visibility": "public"})
	if w.anon.Get("/api/v1/articles/uploaded-zebra?locale=en").Code != 200 {
		t.Fatal("approved upload is visible")
	}
	w.env.Server.Redis.FlushAll(ctx)
	evil := `{"schema_version":1,"article_id":"` + up.String() + `","locale":"en","rev":1,"origin":"human","title":"Evil title","summary":"Evil summary text","body_md":"evil"}`
	w.env.Store.Put(ctx, articles.LocaleKey(up, 1, "en"), []byte(evil), "application/json")
	if r := w.anon.Get("/api/v1/articles/uploaded-zebra?locale=en"); r.Code != 500 || strings.Contains(string(r.Raw), "Evil") {
		t.Errorf("tampered file: %d %s", r.Code, r.Raw)
	}
}

// 14: blocked senders and brand-new accounts hit the anti-spam walls.
func TestMessengerAbuseWalls(t *testing.T) {
	w := newWorld(t)
	conv := w.alice.Post("/api/v1/conversations", map[string]any{"user_id": w.bob.UserID()}).Data()["id"].(string)
	w.bob.Post("/api/v1/conversations/"+conv+"/accept", nil)
	w.bob.Put("/api/v1/users/"+w.alice.UserID()+"/block", nil)
	if send(w.alice, conv, "can you hear me").Code != 403 {
		t.Error("a blocked sender must be refused")
	}
	fresh := w.env.LoginAs("fresh@example.com")
	codes := []int{}
	for i := 0; i < 7; i++ {
		peer := w.env.Client().WithIP(fmt.Sprintf("192.0.2.%d", i+1)) // sign-in attempts are limited per IP, so use different ones
		w.env.SignIn(peer, fmt.Sprintf("peer%d@example.com", i), nil)
		codes = append(codes, fresh.Post("/api/v1/conversations", map[string]any{"user_id": peer.UserID()}).Code)
	}
	if codes[4] != 201 || codes[5] != 429 {
		t.Errorf("new-dialog limit for a new account: %v", codes)
	}
}
