package registry_test

import (
	"context"
	"os"
	"testing"

	"beeloy.com/backend/internal/platform/ids"
	"beeloy.com/backend/internal/registry"
	"beeloy.com/backend/internal/testutil"
	"github.com/google/uuid"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

func TestMappings(t *testing.T) {
	id, author := ids.New(), ids.New()
	type row struct {
		got    registry.Resource
		status registry.Status
		vis    string
		public bool
	}
	cases := []row{
		{registry.FromUser(id, "active", true), registry.StatusActive, "public", true},
		{registry.FromUser(id, "active", false), registry.StatusActive, "restricted", false},
		{registry.FromUser(id, "blocked", true), registry.StatusHidden, "public", false},
		{registry.FromUser(id, "pending_deletion", true), registry.StatusHidden, "public", false},
		{registry.FromUser(id, "deleted", true), registry.StatusRemoved, "public", false},
		{registry.FromArticle(id, &author, "draft", "public"), registry.StatusPending, "private", false},
		{registry.FromArticle(id, &author, "in_review", "public"), registry.StatusPending, "private", false},
		{registry.FromArticle(id, &author, "published", "public"), registry.StatusActive, "public", true},
		{registry.FromArticle(id, &author, "published", "restricted"), registry.StatusActive, "restricted", false},
		{registry.FromArticle(id, &author, "archived", "public"), registry.StatusHidden, "private", false},
		{registry.FromIdea(id, &author), registry.StatusActive, "public", true},
		{registry.FromComment(id, &author, ids.New(), "visible"), registry.StatusActive, "public", true},
		{registry.FromComment(id, &author, ids.New(), "hidden"), registry.StatusHidden, "public", false},
		{registry.FromComment(id, &author, ids.New(), "deleted"), registry.StatusRemoved, "public", false},
	}
	for i, c := range cases {
		if c.got.Status != c.status || c.got.Visibility != c.vis || c.got.PubliclyVisible() != c.public {
			t.Errorf("case %d: %+v", i, c.got)
		}
	}
}

func TestUpsertGetAndCanonicalPath(t *testing.T) {
	pool := testutil.PG(t)
	ctx := context.Background()
	rp := registry.Repo{}
	u := testutil.InsertUser(t, pool, "u")
	pool.Exec(ctx, `INSERT INTO slugs (entity_type, slug, entity_id) VALUES ('user','alice',$1)`, u)
	if err := rp.Upsert(ctx, pool, registry.FromUser(u, "active", true)); err != nil {
		t.Fatal(err)
	}
	// a comment on an idea authored by u
	idea := ids.New()
	pool.Exec(ctx, `INSERT INTO ideas (id, author_id, title, body, category, lang) VALUES ($1,$2,'Some title','Body body body body body','product','en')`, idea, u)
	pool.Exec(ctx, `INSERT INTO slugs (entity_type, slug, entity_id) VALUES ('idea','some-idea',$1)`, idea)
	_ = rp.Upsert(ctx, pool, registry.FromIdea(idea, &u))
	cm := ids.New()
	_ = rp.Upsert(ctx, pool, registry.FromComment(cm, &u, idea, "visible"))

	if p, err := rp.CanonicalPath(ctx, pool, u); err != nil || p != "/people/alice" {
		t.Fatalf("user path: %q %v", p, err)
	}
	if p, _ := rp.CanonicalPath(ctx, pool, cm); p != "/ideas/some-idea#comment-"+cm.String() {
		t.Fatalf("comment path: %q", p)
	}
	// status change is an upsert
	_ = rp.Upsert(ctx, pool, registry.FromUser(u, "blocked", true))
	r, _ := rp.Get(ctx, pool, u)
	if r.PubliclyVisible() {
		t.Fatal("blocked user must not be public")
	}
	if _, err := rp.Get(ctx, pool, uuid.New()); err == nil {
		t.Fatal("unknown id must be not found")
	}
}

func TestReconcileRepairsDrift(t *testing.T) {
	pool := testutil.PG(t)
	ctx := context.Background()
	rp := registry.Repo{}
	u := testutil.InsertUser(t, pool, "u")
	idea := ids.New()
	pool.Exec(ctx, `INSERT INTO ideas (id, author_id, title, body, category, lang) VALUES ($1,$2,'Some title','Body body body body body','product','en')`, idea, u)
	cm := ids.New()
	pool.Exec(ctx, `INSERT INTO comments (id, target_type, target_id, author_id, body, status) VALUES ($1,'idea',$2,$3,'hello','hidden')`, cm, idea, u)
	art := ids.New()
	pool.Exec(ctx, `INSERT INTO articles (id, category_id, source_locale, status, visibility, rev, published_rev) VALUES ($1,'0198f000-0000-7000-8000-000000000001','bg','published','restricted',1,1)`, art)

	n, err := registry.Reconcile(ctx, pool)
	if err != nil || n != 4 {
		t.Fatalf("first reconcile must insert 4 rows: %d %v", n, err)
	}
	if r, _ := rp.Get(ctx, pool, cm); r.Status != registry.StatusHidden || *r.ParentID != idea {
		t.Fatalf("comment row: %+v", r)
	}
	if r, _ := rp.Get(ctx, pool, art); r.Visibility != "restricted" || r.Status != registry.StatusActive {
		t.Fatalf("article row: %+v", r)
	}
	// corrupt one row, reconcile fixes exactly it
	pool.Exec(ctx, `UPDATE resources SET status='active' WHERE id=$1`, cm)
	if n, _ := registry.Reconcile(ctx, pool); n != 1 {
		t.Fatalf("second reconcile must fix 1 row, fixed %d", n)
	}
	if n, _ := registry.Reconcile(ctx, pool); n != 0 {
		t.Fatalf("third reconcile is a no-op, changed %d", n)
	}
}
