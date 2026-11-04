package contentsync_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/articles"
	"beeloy.com/backend/internal/contentsync"
	"beeloy.com/backend/internal/platform/ids"
	"beeloy.com/backend/internal/server/servertest"
	"github.com/google/uuid"
)

type artEnv struct {
	env *servertest.Env
	sy  *contentsync.ArticleSyncer
	ctx context.Context
}

func newArtEnv(t *testing.T) *artEnv {
	env := servertest.New(t)
	s := env.Server
	return &artEnv{env: env, ctx: context.Background(), sy: &contentsync.ArticleSyncer{
		Src: env.Store, Pool: s.Pool, Store: &contentsync.Store{Pool: s.Pool}, Slugs: s.Slugs,
	}}
}

// putArticle writes a manifest and locale files like an external publisher would.
func (a *artEnv) putArticle(t *testing.T, id uuid.UUID, slug string, rev int, files map[string]int) {
	t.Helper()
	m := articles.Manifest{SchemaVersion: 1, ArticleID: id, Slug: slug, CategoryKey: "news", SourceLocale: "en", Rev: rev, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	raw, _ := json.Marshal(m)
	a.env.Store.Put(a.ctx, articles.ManifestKey(id), raw, "application/json")
	for loc, r := range files {
		a.putFile(t, id, r, loc, "human", "Title "+loc, "Body about "+slug+" in "+loc)
	}
}

func (a *artEnv) putFile(t *testing.T, id uuid.UUID, rev int, loc, origin, title, body string) {
	f := articles.LocaleFile{SchemaVersion: 1, ArticleID: id, Locale: loc, Rev: rev, Origin: origin, Title: title + " heading", Summary: "A summary for " + title, BodyMD: body, GeneratedAt: time.Now().UTC()}
	raw, _ := json.Marshal(f)
	a.env.Store.Put(a.ctx, articles.LocaleKey(id, rev, loc), raw, "application/json")
}

func (a *artEnv) count(t *testing.T, sql string, args ...any) int {
	var n int
	if err := a.env.Server.Pool.QueryRow(a.ctx, sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestUploadRegistersButNeverPublishes(t *testing.T) {
	a := newArtEnv(t)
	id := ids.New()
	a.putArticle(t, id, "ready-made-resource", 1, map[string]int{"en": 1, "de": 1, "bg": 1})
	p, f, err := a.sy.RunOnce(a.ctx)
	if err != nil || p != 1 || f != 0 {
		t.Fatalf("run: %d %d %v", p, f, err)
	}
	var status string
	a.env.Server.Pool.QueryRow(a.ctx, `SELECT status FROM articles WHERE id=$1`, id).Scan(&status)
	if status != "in_review" {
		t.Fatalf("status after sync: %s", status)
	}
	if n := a.count(t, `SELECT count(*) FROM article_revisions WHERE article_id=$1`, id); n != 3 {
		t.Errorf("revisions: %d", n)
	}
	if n := a.count(t, `SELECT count(*) FROM article_locales WHERE article_id=$1`, id); n != 0 {
		t.Errorf("live index must be empty before approval: %d", n)
	}
	anon := a.env.Client()
	if len(anon.Get("/api/v1/articles?locale=en").List()) != 0 || anon.Get("/api/v1/articles/ready-made-resource?locale=en").Code != 404 {
		t.Error("uploaded article must not be visible")
	}
	if len(anon.Get("/api/v1/search/articles?q=ready-made&locale=en").List()) != 0 {
		t.Error("uploaded article must not be searchable")
	}
	mod := a.env.LoginAs("mod@example.com")
	a.env.Grant(mod.UserID(), acl.RoleModerator, nil)
	if q := mod.Get("/api/v1/mod/articles").List(); len(q) != 1 {
		t.Fatalf("queue: %d", len(q))
	}
	if r := mod.Post("/api/v1/mod/articles/"+id.String()+"/publish", map[string]any{"rev": 1, "visibility": "public"}); r.Code != 200 {
		t.Fatalf("publish: %d %s", r.Code, r.Raw)
	}
	v := anon.Get("/api/v1/articles/ready-made-resource?locale=de")
	if v.Code != 200 || v.Data()["locale"] != "de" {
		t.Fatalf("de: %d %s", v.Code, v.Raw)
	}
	if h := anon.Get("/api/v1/search/articles?q=resource&locale=bg").List(); len(h) != 1 {
		t.Errorf("search after approval: %v", h)
	}
}

func TestSecondRunIsNoOpAndChangesAreDetected(t *testing.T) {
	a := newArtEnv(t)
	id := ids.New()
	a.putArticle(t, id, "stable-article", 1, map[string]int{"en": 1})
	if p, _, _ := a.sy.RunOnce(a.ctx); p != 1 {
		t.Fatal("first run")
	}
	if p, f, _ := a.sy.RunOnce(a.ctx); p != 0 || f != 0 {
		t.Fatalf("unchanged S3 must give no work: %d %d", p, f)
	}
	a.putFile(t, id, 1, "fr", "machine", "Titre", "Texte en francais")
	if p, _, _ := a.sy.RunOnce(a.ctx); p != 1 {
		t.Fatal("new file must be picked up")
	}
	var origin string
	a.env.Server.Pool.QueryRow(a.ctx, `SELECT origin FROM article_revisions WHERE article_id=$1 AND locale='fr'`, id).Scan(&origin)
	if origin != "machine" {
		t.Errorf("origin: %s", origin)
	}
}

func TestInvalidFileAndMissingManifestDoNotBlockOthers(t *testing.T) {
	a := newArtEnv(t)
	good, bad, noManifest := ids.New(), ids.New(), ids.New()
	a.putArticle(t, good, "good-article", 1, map[string]int{"en": 1})
	a.putArticle(t, bad, "half-broken", 1, map[string]int{"en": 1, "de": 1})
	a.env.Store.Put(a.ctx, articles.LocaleKey(bad, 1, "de"), []byte(`{"schema_version":1,"title":"x"}`), "application/json")
	a.env.Store.Put(a.ctx, articles.LocaleKey(noManifest, 1, "en"), []byte(`{}`), "application/json")
	p, f, err := a.sy.RunOnce(a.ctx)
	if err != nil || p != 1 || f != 2 {
		t.Fatalf("processed=%d failed=%d err=%v", p, f, err)
	}
	// an invalid file fails the whole article: nothing is half-registered and the next run tries again
	if n := a.count(t, `SELECT count(*) FROM article_revisions WHERE article_id=$1`, bad); n != 0 {
		t.Errorf("half-broken article must not be registered: %d", n)
	}
	if _, f, _ := a.sy.RunOnce(a.ctx); f != 2 {
		t.Errorf("a failed article is retried without any S3 change: failed=%d", f)
	}
	a.putFile(t, bad, 1, "de", "human", "Deutsch", "Korrigierter Text")
	a.env.Store.Delete(articles.LocaleKey(bad, 1, "de"))
	a.putFile(t, bad, 1, "de", "human", "Deutsch", "Korrigierter Text")
	if p, _, _ := a.sy.RunOnce(a.ctx); p != 1 {
		t.Errorf("fixed article is picked up: %d", p)
	}
	if n := a.count(t, `SELECT count(*) FROM articles WHERE id=$1`, noManifest); n != 0 {
		t.Error("article without manifest must not be created")
	}
}

func TestSlugConflictSkipsOnlyThatArticle(t *testing.T) {
	a := newArtEnv(t)
	taken := ids.New()
	a.env.Server.Pool.Exec(a.ctx, `INSERT INTO slugs (entity_type, slug, entity_id) VALUES ('article','contested',$1)`, ids.New())
	a.putArticle(t, taken, "contested", 1, map[string]int{"en": 1})
	ok := ids.New()
	a.putArticle(t, ok, "free-slug", 1, map[string]int{"en": 1})
	p, f, _ := a.sy.RunOnce(a.ctx)
	if p != 1 || f != 1 {
		t.Fatalf("processed=%d failed=%d", p, f)
	}
	if a.count(t, `SELECT count(*) FROM articles WHERE id=$1`, taken) != 0 {
		t.Error("conflicting article must be rolled back entirely")
	}
}

func TestPublishedArticleKeepsApprovedRevision(t *testing.T) {
	a := newArtEnv(t)
	id := ids.New()
	a.putArticle(t, id, "live-article", 1, map[string]int{"en": 1})
	a.sy.RunOnce(a.ctx)
	mod := a.env.LoginAs("mod@example.com")
	a.env.Grant(mod.UserID(), acl.RoleModerator, nil)
	mod.Post("/api/v1/mod/articles/"+id.String()+"/publish", map[string]any{"rev": 1, "visibility": "public"})
	anon := a.env.Client()
	a.putArticle(t, id, "live-article", 2, map[string]int{"en": 2})
	a.putFile(t, id, 1, "de", "human", "Deutsch", "Deutscher Text")
	a.putFile(t, id, 3, "en", "human", "Future", "Future text") // rev 3 > manifest.rev
	if p, _, _ := a.sy.RunOnce(a.ctx); p != 1 {
		t.Fatal("run")
	}
	var status string
	var pub, rev int
	a.env.Server.Pool.QueryRow(a.ctx, `SELECT status, published_rev, rev FROM articles WHERE id=$1`, id).Scan(&status, &pub, &rev)
	if status != "published" || pub != 1 || rev != 2 {
		t.Fatalf("sync must not touch publication state: %s %d %d", status, pub, rev)
	}
	if v := anon.Get("/api/v1/articles/live-article?locale=en"); v.Data()["title"] != "Title en heading" {
		t.Fatalf("public text must stay at rev 1: %s", v.Raw)
	}
	if v := anon.Get("/api/v1/articles/live-article?locale=de"); v.Data()["locale"] != "de" || v.Data()["body_md"] != "Deutscher Text" {
		t.Fatalf("translation of the approved revision must go live: %s", v.Raw)
	}
	if a.count(t, `SELECT count(*) FROM article_revisions WHERE article_id=$1 AND rev=3`, id) != 0 {
		t.Error("files beyond manifest.rev are ignored")
	}
	if q := mod.Get("/api/v1/mod/articles").List(); len(q) != 1 || q[0].(map[string]any)["pending"] != true {
		t.Errorf("rev 2 must wait in the queue: %v", q)
	}
}

func TestApiCreatedArticleKeepsStateAfterSync(t *testing.T) {
	a := newArtEnv(t)
	ed := a.env.LoginAs("ed@example.com")
	a.env.Grant(ed.UserID(), acl.RoleEditor, nil)
	r := ed.Post("/api/v1/editor/articles", map[string]any{"category_id": servertest.CatNews, "source_locale": "en", "title_for_slug": "Made in the editor"})
	id := r.Data()["id"].(string)
	ed.Put("/api/v1/editor/articles/"+id+"/source", map[string]any{"title": "Editor title", "summary": "An editor summary text.", "body_md": "body"})
	before := a.count(t, `SELECT count(*) FROM article_revisions`)
	if p, f, err := a.sy.RunOnce(a.ctx); err != nil || f != 0 || p != 1 {
		t.Fatalf("%d %d %v", p, f, err)
	}
	var status string
	a.env.Server.Pool.QueryRow(a.ctx, `SELECT status FROM articles WHERE id=$1`, id).Scan(&status)
	if status != "draft" || a.count(t, `SELECT count(*) FROM article_revisions`) != before {
		t.Errorf("sync must not change an API-created draft: %s", status)
	}
}

func TestConcurrentArticleRuns(t *testing.T) {
	a := newArtEnv(t)
	for i := 0; i < 5; i++ {
		a.putArticle(t, ids.New(), fmt.Sprintf("concurrent-%d", i), 1, map[string]int{"en": 1})
	}
	var wg sync.WaitGroup
	total := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p, _, _ := a.sy.RunOnce(a.ctx); total <- p }()
	}
	wg.Wait()
	close(total)
	sum := 0
	for p := range total {
		sum += p
	}
	if sum != 5 || a.count(t, `SELECT count(*) FROM articles`) != 5 {
		t.Fatalf("each article must be processed exactly once, processed=%d", sum)
	}
}

func TestAlteredRevisionFileIsRejected(t *testing.T) {
	a := newArtEnv(t)
	id := ids.New()
	a.putArticle(t, id, "tamper-target", 1, map[string]int{"en": 1})
	if p, _, _ := a.sy.RunOnce(a.ctx); p != 1 {
		t.Fatal("first run")
	}
	var before string
	a.env.Server.Pool.QueryRow(a.ctx, `SELECT sha256 FROM article_revisions WHERE article_id=$1 AND locale='en'`, id).Scan(&before)
	a.env.Store.Delete(articles.LocaleKey(id, 1, "en"))
	a.putFile(t, id, 1, "en", "human", "Altered title", "Altered text that was never approved")
	if _, f, _ := a.sy.RunOnce(a.ctx); f != 1 {
		t.Fatalf("an altered file must fail the article: failed=%d", f)
	}
	var after string
	a.env.Server.Pool.QueryRow(a.ctx, `SELECT sha256 FROM article_revisions WHERE article_id=$1 AND locale='en'`, id).Scan(&after)
	if after != before {
		t.Fatal("the registered hash must never follow a changed file")
	}
	// the operator is told: one audit event, not one per retry
	a.sy.RunOnce(a.ctx)
	a.sy.RunOnce(a.ctx)
	var n int
	a.env.Server.Pool.QueryRow(a.ctx, `SELECT count(*) FROM audit_events WHERE action='content.integrity_violation' AND target_id=$1`, id.String()).Scan(&n)
	if n != 1 {
		t.Fatalf("integrity events: %d, want exactly 1", n)
	}
}

func TestManifestCannotLowerRegisteredRev(t *testing.T) {
	a := newArtEnv(t)
	id := ids.New()
	a.putArticle(t, id, "rev-guard", 2, map[string]int{"en": 2})
	a.sy.RunOnce(a.ctx)
	a.putArticle(t, id, "rev-guard", 1, map[string]int{"en": 1})
	if _, f, _ := a.sy.RunOnce(a.ctx); f != 1 {
		t.Fatalf("an older manifest must be refused: failed=%d", f)
	}
	var rev int
	a.env.Server.Pool.QueryRow(a.ctx, `SELECT rev FROM articles WHERE id=$1`, id).Scan(&rev)
	if rev != 2 {
		t.Fatalf("rev must not decrease: %d", rev)
	}
}
