package articles_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

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
	env                      *servertest.Env
	mod, editor, catEd, anon *servertest.Client
	member                   *servertest.Client
	memberID, catEdID, modID string
}

func newWorld(t *testing.T) *world {
	env := servertest.New(t)
	w := &world{env: env, anon: env.Client()}
	w.mod = env.LoginAs("mod@example.com")
	w.modID = w.mod.UserID()
	env.Grant(w.modID, acl.RoleModerator, nil)
	w.editor = env.LoginAs("editor@example.com")
	env.Grant(w.editor.UserID(), acl.RoleEditor, nil)
	w.catEd = env.LoginAs("cateditor@example.com")
	w.catEdID = w.catEd.UserID()
	news := servertest.CatNews
	env.Grant(w.catEdID, acl.RoleEditor, &news)
	w.member = env.LoginAs("member@example.com")
	w.memberID = w.member.UserID()
	return w
}

func (w *world) create(t *testing.T, c *servertest.Client, cat, title string) string {
	r := c.Post("/api/v1/editor/articles", map[string]any{"category_id": cat, "source_locale": "en", "title_for_slug": title})
	if r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Raw)
	}
	return r.Data()["id"].(string)
}

func (w *world) save(t *testing.T, c *servertest.Client, id, title, body string) int {
	r := c.Put("/api/v1/editor/articles/"+id+"/source", map[string]any{"title": title, "summary": "A short summary of the article.", "body_md": body})
	if r.Code != 200 {
		t.Fatalf("save: %d %s", r.Code, r.Raw)
	}
	return int(r.Data()["rev"].(float64))
}

func (w *world) publish(t *testing.T, id string, rev int, vis string, viewers ...string) *servertest.Resp {
	body := map[string]any{"rev": rev, "visibility": vis}
	if len(viewers) > 0 {
		body["viewer_ids"] = viewers
	}
	return w.mod.Post("/api/v1/mod/articles/"+id+"/publish", body)
}

func TestEditorCannotPublishModeratorCan(t *testing.T) {
	w := newWorld(t)
	id := w.create(t, w.editor, servertest.CatNews, "Quantum computing news")
	rev := w.save(t, w.editor, id, "Quantum computing", "Body about qubits.")
	if rev != 1 {
		t.Fatalf("first rev: %d", rev)
	}
	// draft is invisible everywhere
	if w.anon.Get("/api/v1/articles?locale=en").List() == nil || len(w.anon.Get("/api/v1/articles?locale=en").List()) != 0 {
		t.Error("draft must not be listed")
	}
	slug := w.editor.Get("/api/v1/editor/articles/" + id).Data()["article"].(map[string]any)["slug"].(string)
	if w.anon.Get("/api/v1/articles/"+slug+"?locale=en").Code != 404 {
		t.Error("draft must be 404 by slug")
	}
	// editor cannot publish; submit works
	if r := w.editor.Post("/api/v1/mod/articles/"+id+"/publish", map[string]any{"rev": 1, "visibility": "public"}); r.Code != 403 {
		t.Fatalf("editor publish must be 403: %d %s", r.Code, r.Raw)
	}
	if r := w.editor.Post("/api/v1/editor/articles/"+id+"/submit", nil); r.Code != 200 || r.Data()["status"] != "in_review" {
		t.Fatalf("submit: %d %s", r.Code, r.Raw)
	}
	if r := w.member.Post("/api/v1/mod/articles/"+id+"/publish", map[string]any{"rev": 1, "visibility": "public"}); r.Code != 404 {
		t.Errorf("member must not see or publish the article (404): %d", r.Code)
	}
	// queue contains it, moderator publishes
	if q := w.mod.Get("/api/v1/mod/articles").List(); len(q) != 1 {
		t.Fatalf("queue: %d", len(q))
	}
	if r := w.publish(t, id, 1, "public"); r.Code != 200 || r.Data()["status"] != "published" || r.Data()["published_rev"].(float64) != 1 {
		t.Fatalf("publish: %d %s", r.Code, r.Raw)
	}
	pub := w.anon.Get("/api/v1/articles/" + slug + "?locale=en")
	if pub.Code != 200 || pub.Data()["body_md"] != "Body about qubits." || pub.JSON["canonical_slug"] != slug || pub.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("public read: %d %s %v", pub.Code, pub.Raw, pub.Header)
	}
	if lst := w.anon.Get("/api/v1/articles?locale=en").List(); len(lst) != 1 {
		t.Fatalf("list: %d", len(lst))
	}
	// audit + outbox
	var n int
	w.env.Server.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='article.publish'`).Scan(&n)
	if n != 1 {
		t.Error("publish must be audited")
	}
	w.env.Server.Pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE event_type IN ('article.published','article.submitted')`).Scan(&n)
	if n != 2 {
		t.Errorf("outbox events: %d", n)
	}
}

func TestNewRevisionDoesNotChangePublicText(t *testing.T) {
	w := newWorld(t)
	id := w.create(t, w.editor, servertest.CatNews, "Stable article")
	w.save(t, w.editor, id, "Original title", "Original body")
	w.editor.Post("/api/v1/editor/articles/"+id+"/submit", nil)
	w.publish(t, id, 1, "public")
	slug := w.editor.Get("/api/v1/editor/articles/" + id).Data()["article"].(map[string]any)["slug"].(string)

	if rev := w.save(t, w.editor, id, "Edited title", "Edited body"); rev != 2 {
		t.Fatalf("rev: %d", rev)
	}
	if v := w.anon.Get("/api/v1/articles/" + slug + "?locale=en"); v.Data()["body_md"] != "Original body" || v.Data()["title"] != "Original title" {
		t.Fatalf("public text must stay at the approved revision: %s", v.Raw)
	}
	q := w.mod.Get("/api/v1/mod/articles").List()
	if len(q) != 1 || q[0].(map[string]any)["pending"] != true {
		t.Fatalf("pending revision must appear in the queue: %v", q)
	}
	// moderator previews rev 2, then approves it
	if p := w.mod.Get("/api/v1/editor/articles/" + id + "/revisions/2/en"); p.Code != 200 || p.Data()["body_md"] != "Edited body" {
		t.Fatalf("preview: %d %s", p.Code, p.Raw)
	}
	if r := w.publish(t, id, 2, "public"); r.Code != 200 {
		t.Fatal(r.Raw)
	}
	if v := w.anon.Get("/api/v1/articles/" + slug + "?locale=en"); v.Data()["body_md"] != "Edited body" {
		t.Fatalf("after approval: %s", v.Raw)
	}
}

func TestTranslationsFallbackAndHumanImmutability(t *testing.T) {
	w := newWorld(t)
	id := w.create(t, w.editor, servertest.CatNews, "Multilingual")
	w.save(t, w.editor, id, "English title", "English body")
	w.editor.Post("/api/v1/editor/articles/"+id+"/submit", nil)
	w.publish(t, id, 1, "public")
	slug := w.editor.Get("/api/v1/editor/articles/" + id).Data()["article"].(map[string]any)["slug"].(string)

	// translation for the already approved revision appears automatically
	de := map[string]any{"title": "Deutscher Titel", "summary": "Eine kurze Zusammenfassung.", "body_md": "Deutscher Text"}
	if r := w.editor.Put("/api/v1/editor/articles/"+id+"/translations/de", de); r.Code != 204 {
		t.Fatalf("put translation: %d %s", r.Code, r.Raw)
	}
	v := w.anon.Get("/api/v1/articles/" + slug + "?locale=de")
	if v.Code != 200 || v.Data()["locale"] != "de" || v.Data()["origin"] != "human" || v.Data()["body_md"] != "Deutscher Text" || v.Data()["fallback_from"] != nil {
		t.Fatalf("de: %d %s", v.Code, v.Raw)
	}
	avail := v.Data()["available_locales"].([]any)
	if len(avail) != 2 {
		t.Errorf("available locales: %v", avail)
	}
	// fallback to en for a missing language
	f := w.anon.Get("/api/v1/articles/" + slug + "?locale=fr")
	if f.Data()["locale"] != "en" || f.Data()["fallback_from"] != "fr" {
		t.Fatalf("fallback: %s", f.Raw)
	}
	// human file is never replaced; source locale cannot be a "translation"; unknown locale rejected
	if r := w.editor.Put("/api/v1/editor/articles/"+id+"/translations/de", de); r.Code != 409 {
		t.Errorf("replace human: %d", r.Code)
	}
	if r := w.editor.Put("/api/v1/editor/articles/"+id+"/translations/en", de); r.Code != 422 {
		t.Errorf("translation into source locale: %d", r.Code)
	}
	if r := w.editor.Put("/api/v1/editor/articles/"+id+"/translations/tr", de); r.Code != 422 {
		t.Errorf("non-supported locale: %d", r.Code)
	}
	// list falls back per article; unknown locale on public endpoints is rejected
	if lst := w.anon.Get("/api/v1/articles?locale=fr").List(); len(lst) != 1 || lst[0].(map[string]any)["locale"] != "en" || lst[0].(map[string]any)["fallback_from"] != "fr" {
		t.Fatalf("list fallback: %v", lst)
	}
	if w.anon.Get("/api/v1/articles?locale=xx").Code != 422 {
		t.Error("unknown locale must be 422")
	}
}

func TestRestrictedVisibility(t *testing.T) {
	w := newWorld(t)
	id := w.create(t, w.editor, servertest.CatNews, "Secret club")
	w.save(t, w.editor, id, "Members only title", "Members only body about zebras")
	w.editor.Post("/api/v1/editor/articles/"+id+"/submit", nil)
	if r := w.publish(t, id, 1, "restricted", w.memberID); r.Code != 200 || r.Data()["visibility"] != "restricted" {
		t.Fatalf("publish restricted: %d %s", r.Code, r.Raw)
	}
	if r := w.publish(t, id, 1, "restricted", uuid.NewString()); r.Code != 422 {
		t.Errorf("unknown viewer must be rejected: %d", r.Code)
	}
	slug := w.editor.Get("/api/v1/editor/articles/" + id).Data()["article"].(map[string]any)["slug"].(string)
	url := "/api/v1/articles/" + slug + "?locale=en"
	other := w.env.LoginAs("other@example.com")
	for name, c := range map[string]*servertest.Client{"anon": w.anon, "other member": other} {
		if c.Get(url).Code != 404 {
			t.Errorf("%s must get 404", name)
		}
	}
	r := w.member.Get(url)
	if r.Code != 200 || r.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("viewer: %d %v", r.Code, r.Header)
	}
	if w.editor.Get(url).Code != 200 || w.mod.Get(url).Code != 200 {
		t.Error("staff can read restricted articles")
	}
	// not in public list, not in search, visible in /me/articles
	if len(w.anon.Get("/api/v1/articles?locale=en").List()) != 0 {
		t.Error("restricted must not be listed publicly")
	}
	if len(w.anon.Get("/api/v1/search/articles?q=zebras&locale=en").List()) != 0 {
		t.Error("restricted must not be searchable")
	}
	if my := w.member.Get("/api/v1/me/articles?locale=en").List(); len(my) != 1 {
		t.Errorf("/me/articles: %d", len(my))
	}
	if my := other.Get("/api/v1/me/articles?locale=en").List(); len(my) != 0 {
		t.Errorf("other /me/articles: %d", len(my))
	}
}

func TestCategoryScopeAndNoRightsLeak(t *testing.T) {
	w := newWorld(t)
	// category editor (news only) cannot create in product
	if r := w.catEd.Post("/api/v1/editor/articles", map[string]any{"category_id": servertest.CatProduct, "source_locale": "en", "title_for_slug": "nope nope"}); r.Code != 403 {
		t.Fatalf("out-of-scope create: %d %s", r.Code, r.Raw)
	}
	inNews := w.create(t, w.catEd, servertest.CatNews, "News piece")
	inProduct := w.create(t, w.editor, servertest.CatProduct, "Product piece")
	if r := w.catEd.Get("/api/v1/editor/articles/" + inProduct); r.Code != 404 {
		t.Errorf("foreign category article must be hidden: %d", r.Code)
	}
	if r := w.catEd.Put("/api/v1/editor/articles/"+inProduct+"/source", map[string]any{"title": "Hacked title", "summary": "A short summary text.", "body_md": "x"}); r.Code != 404 {
		t.Errorf("write to foreign article: %d", r.Code)
	}
	list := w.catEd.Get("/api/v1/editor/articles").List()
	if len(list) != 1 || list[0].(map[string]any)["id"] != inNews {
		t.Errorf("category editor must see only own category: %v", list)
	}
	if len(w.editor.Get("/api/v1/editor/articles").List()) != 2 {
		t.Error("global editor sees all")
	}
	// moving an article into a category without rights is refused
	if r := w.catEd.Patch("/api/v1/editor/articles/"+inNews, map[string]any{"category_id": servertest.CatProduct}); r.Code != 403 {
		t.Errorf("move to foreign category: %d", r.Code)
	}
	// plain members and anonymous
	if w.member.Get("/api/v1/editor/articles").Code != 403 || w.anon.Get("/api/v1/editor/articles").Code != 401 {
		t.Error("editor list requires the editor role")
	}
	if w.member.Get("/api/v1/mod/articles").Code != 403 {
		t.Error("queue requires moderator")
	}
}

func TestPublishNeedsSourceAndStates(t *testing.T) {
	w := newWorld(t)
	id := w.create(t, w.editor, servertest.CatNews, "Empty article")
	if r := w.publish(t, id, 1, "public"); r.Code != 409 {
		t.Fatalf("publish without text: %d %s", r.Code, r.Raw)
	}
	if r := w.editor.Post("/api/v1/editor/articles/"+id+"/submit", nil); r.Code != 409 {
		t.Fatalf("submit without text: %d", r.Code)
	}
	w.save(t, w.editor, id, "Some title", "Some body")
	if r := w.publish(t, id, 5, "public"); r.Code != 422 {
		t.Errorf("rev beyond latest: %d", r.Code)
	}
	if r := w.publish(t, id, 1, "everyone"); r.Code != 422 {
		t.Errorf("bad visibility: %d", r.Code)
	}
	// reject only from in_review
	if r := w.mod.Post("/api/v1/mod/articles/"+id+"/reject", map[string]any{"note": "no"}); r.Code != 409 {
		t.Errorf("reject draft: %d", r.Code)
	}
	w.editor.Post("/api/v1/editor/articles/"+id+"/submit", nil)
	if r := w.mod.Post("/api/v1/mod/articles/"+id+"/reject", map[string]any{"note": "needs work"}); r.Code != 200 || r.Data()["status"] != "draft" {
		t.Errorf("reject: %d %s", r.Code, r.Raw)
	}
	w.editor.Post("/api/v1/editor/articles/"+id+"/submit", nil)
	w.publish(t, id, 1, "public")
	// unpublish hides it
	slug := w.editor.Get("/api/v1/editor/articles/" + id).Data()["article"].(map[string]any)["slug"].(string)
	if r := w.mod.Post("/api/v1/mod/articles/"+id+"/unpublish", nil); r.Code != 200 || r.Data()["status"] != "archived" {
		t.Fatalf("unpublish: %d %s", r.Code, r.Raw)
	}
	if w.anon.Get("/api/v1/articles/"+slug+"?locale=en").Code != 404 {
		t.Error("archived article must be hidden")
	}
	if r := w.mod.Post("/api/v1/mod/articles/"+id+"/unpublish", nil); r.Code != 409 {
		t.Errorf("double unpublish: %d", r.Code)
	}
}

func TestParallelSavesGetDistinctRevisions(t *testing.T) {
	w := newWorld(t)
	id := w.create(t, w.editor, servertest.CatNews, "Parallel")
	var wg sync.WaitGroup
	revs := make(chan int, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := w.env.Client()
			c.Cookies = w.editor.Cookies
			r := c.Put("/api/v1/editor/articles/"+id+"/source", map[string]any{"title": fmt.Sprintf("Title %d", i), "summary": "A short summary text.", "body_md": "body"})
			if r.Code != 200 {
				t.Errorf("save %d: %d %s", i, r.Code, r.Raw)
				return
			}
			revs <- int(r.Data()["rev"].(float64))
		}(i)
	}
	wg.Wait()
	close(revs)
	seen := map[int]bool{}
	for r := range revs {
		if seen[r] {
			t.Fatalf("duplicate revision %d", r)
		}
		seen[r] = true
		if ok, _ := w.env.Store.Exists(context.Background(), articles.LocaleKey(uuid.MustParse(id), r, "en")); !ok {
			t.Errorf("file for rev %d missing", r)
		}
	}
	if len(seen) != 6 {
		t.Fatalf("revisions: %v", seen)
	}
}

func TestS3IntegrityIsEnforced(t *testing.T) {
	w := newWorld(t)
	id := w.create(t, w.editor, servertest.CatNews, "Tamper")
	w.save(t, w.editor, id, "Honest title", "Honest body")
	w.editor.Post("/api/v1/editor/articles/"+id+"/submit", nil)
	w.publish(t, id, 1, "public")
	slug := w.editor.Get("/api/v1/editor/articles/" + id).Data()["article"].(map[string]any)["slug"].(string)
	// somebody replaces the file in the bucket
	evil := `{"schema_version":1,"article_id":"` + id + `","locale":"en","rev":1,"origin":"human","title":"Evil title","summary":"Evil summary text","body_md":"<script>evil</script>"}`
	w.env.Store.Put(context.Background(), articles.LocaleKey(uuid.MustParse(id), 1, "en"), []byte(evil), "application/json")
	r := w.anon.Get("/api/v1/articles/" + slug + "?locale=en")
	if r.Code != 500 || strings.Contains(string(r.Raw), "evil") {
		t.Fatalf("tampered file must not be served: %d %s", r.Code, r.Raw)
	}
}

func TestSearchOnlyApprovedPublicContent(t *testing.T) {
	w := newWorld(t)
	pubID := w.create(t, w.editor, servertest.CatNews, "Public piece")
	w.save(t, w.editor, pubID, "Public piece title", "The body mentions chrysanthemum and quantum mechanics.")
	draftID := w.create(t, w.editor, servertest.CatNews, "Draft piece")
	w.save(t, w.editor, draftID, "Draft title", "A draft with the word chrysanthemum too.")
	w.editor.Post("/api/v1/editor/articles/"+pubID+"/submit", nil)
	w.publish(t, pubID, 1, "public")

	hits := w.anon.Get("/api/v1/search/articles?q=chrysanthemum&locale=en").List()
	if len(hits) != 1 || hits[0].(map[string]any)["title"] != "Public piece title" {
		t.Fatalf("fts: %v", hits)
	}
	// substring query falls back to trigram matching on title+summary
	if h := w.anon.Get("/api/v1/search/articles?q=ubli&locale=en").List(); len(h) != 1 {
		t.Errorf("trigram fallback: %v", h)
	}
	if h := w.anon.Get("/api/v1/search/articles?q=chrysanthemum&locale=bg").List(); len(h) != 0 {
		t.Errorf("only the requested locale is searched: %v", h)
	}
	if r := w.anon.Get("/api/v1/search/articles?q=a&locale=en"); r.Code != 422 {
		t.Errorf("1-char query: %d", r.Code)
	}
	// a new, unapproved revision with new words is not searchable
	w.save(t, w.editor, pubID, "Public piece title", "Now it talks about hydrangea instead.")
	if h := w.anon.Get("/api/v1/search/articles?q=hydrangea&locale=en").List(); len(h) != 0 {
		t.Errorf("unapproved text must not be searchable: %v", h)
	}
	w.publish(t, pubID, 2, "public")
	if h := w.anon.Get("/api/v1/search/articles?q=hydrangea&locale=en").List(); len(h) != 1 {
		t.Errorf("approved text must be searchable: %v", h)
	}
	if h := w.anon.Get("/api/v1/search/articles?q=chrysanthemum&locale=en").List(); len(h) != 0 {
		t.Errorf("old text is gone from the index: %v", h)
	}
}

func TestValidationAndStoreFailure(t *testing.T) {
	w := newWorld(t)
	id := w.create(t, w.editor, servertest.CatNews, "Validation")
	for name, body := range map[string]map[string]any{
		"short title":   {"title": "ab", "summary": "A short summary text.", "body_md": "x"},
		"short summary": {"title": "Fine title", "summary": "short", "body_md": "x"},
		"empty body":    {"title": "Fine title", "summary": "A short summary text.", "body_md": ""},
	} {
		if r := w.editor.Put("/api/v1/editor/articles/"+id+"/source", body); r.Code != 422 {
			t.Errorf("%s: %d", name, r.Code)
		}
	}
	if r := w.editor.Post("/api/v1/editor/articles", map[string]any{"category_id": servertest.CatNews, "source_locale": "tr", "title_for_slug": "x y z"}); r.Code != 422 {
		t.Errorf("bad source locale: %d", r.Code)
	}
	if r := w.editor.Post("/api/v1/editor/articles", map[string]any{"category_id": servertest.CatNews, "source_locale": "en", "slug": "admin-ok", "title_for_slug": "x"}); r.Code != 201 {
		t.Errorf("explicit slug: %d %s", r.Code, r.Raw)
	}
	if r := w.editor.Post("/api/v1/editor/articles", map[string]any{"category_id": servertest.CatNews, "source_locale": "en", "slug": "admin-ok"}); r.Code != 409 {
		t.Errorf("duplicate slug: %d", r.Code)
	}
	// S3 outage -> 503, nothing registered
	w.env.Store.FailPut = fmt.Errorf("s3 down")
	if r := w.editor.Put("/api/v1/editor/articles/"+id+"/source", map[string]any{"title": "Fine title", "summary": "A short summary text.", "body_md": "x"}); r.Code != 503 {
		t.Errorf("S3 outage: %d %s", r.Code, r.Raw)
	}
	var n int
	w.env.Server.Pool.QueryRow(context.Background(), `SELECT count(*) FROM article_revisions`).Scan(&n)
	if n != 0 {
		t.Errorf("no revision may be registered when S3 failed: %d", n)
	}
}

// The blog ("article" category, migration 0014) is a separate section: the news list can leave it out and the blog list keeps only it.
func TestBlogCategoryListsAreSeparate(t *testing.T) {
	w := newWorld(t)
	const catArticle = "0198f000-0000-7000-8000-000000000004"
	for cat, title := range map[string]string{servertest.CatNews: "News item here", catArticle: "Blog post here"} {
		id := w.create(t, w.editor, cat, title)
		if r := w.publish(t, id, w.save(t, w.editor, id, title, "Body text of the piece."), "public"); r.Code != 200 {
			t.Fatalf("publish %s: %d %s", title, r.Code, r.Raw)
		}
	}
	keys := func(q string) []string {
		var out []string
		for _, it := range w.anon.Get("/api/v1/articles?locale=en" + q).List() {
			out = append(out, it.(map[string]any)["category_key"].(string))
		}
		return out
	}
	if got := keys(""); len(got) != 2 {
		t.Fatalf("all: %v", got)
	}
	if got := keys("&exclude_category=article"); len(got) != 1 || got[0] != "news" {
		t.Errorf("exclude: %v", got)
	}
	if got := keys("&category=article"); len(got) != 1 || got[0] != "article" {
		t.Errorf("only: %v", got)
	}
}
