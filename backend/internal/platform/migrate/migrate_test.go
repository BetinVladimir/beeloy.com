package migrate_test

import (
	"context"
	"os"
	"testing"

	"beeloy.com/backend/internal/platform/migrate"
	"beeloy.com/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

func TestUpDownUp(t *testing.T) {
	url := testutil.EmptyPGURL(t)
	dir := testutil.MigrationsDir()
	if err := migrate.Up(url, dir); err != nil {
		t.Fatal("up:", err)
	}
	if v, dirty, err := migrate.Version(url, dir); err != nil || dirty || v != 13 {
		t.Fatalf("version: %d %v %v", v, dirty, err)
	}
	if err := migrate.DownAll(url, dir); err != nil {
		t.Fatal("down:", err)
	}
	pool, _ := pgxpool.New(context.Background(), url)
	defer pool.Close()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name <> 'schema_migrations'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("tables left after down: %d %v", n, err)
	}
	if err := migrate.Up(url, dir); err != nil {
		t.Fatal("up again:", err)
	}
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) error {
	t.Helper()
	_, err := pool.Exec(context.Background(), sql, args...)
	return err
}

const (
	u1 = "0198f111-0000-7000-8000-000000000001"
	u2 = "0198f111-0000-7000-8000-000000000002"
)

func TestInvariants(t *testing.T) {
	pool := testutil.PG(t)
	for _, id := range []string{u1, u2} {
		if err := exec(t, pool, "INSERT INTO users (id, nickname) VALUES ($1,'n')", id); err != nil {
			t.Fatal(err)
		}
	}
	// 1. unique (provider, subject)
	if err := exec(t, pool, "INSERT INTO auth_identities (id,user_id,provider,subject) VALUES (gen_random_uuid(),$1,'email_otp','a@b.c')", u1); err != nil {
		t.Fatal(err)
	}
	if err := exec(t, pool, "INSERT INTO auth_identities (id,user_id,provider,subject) VALUES (gen_random_uuid(),$1,'email_otp','a@b.c')", u2); err == nil {
		t.Error("duplicate identity must fail")
	}
	if err := exec(t, pool, "INSERT INTO auth_identities (id,user_id,provider,subject) VALUES (gen_random_uuid(),$1,'github','x')", u1); err == nil {
		t.Error("unknown provider must fail")
	}
	// 2. one current slug per entity
	if err := exec(t, pool, "INSERT INTO slugs (entity_type,slug,entity_id) VALUES ('user','alice',$1)", u1); err != nil {
		t.Fatal(err)
	}
	if err := exec(t, pool, "INSERT INTO slugs (entity_type,slug,entity_id) VALUES ('user','alice2',$1)", u1); err == nil {
		t.Error("two current slugs for one entity must fail")
	}
	if err := exec(t, pool, "INSERT INTO slugs (entity_type,slug,entity_id) VALUES ('user','Bad_Slug',$1)", u2); err == nil {
		t.Error("slug format check")
	}
	// 3. conversation ordering
	if err := exec(t, pool, "INSERT INTO conversations (id,user_low,user_high,initiator_id) VALUES (gen_random_uuid(),$2,$1,$1)", u1, u2); err == nil {
		t.Error("user_low >= user_high must fail")
	}
	if err := exec(t, pool, "INSERT INTO conversations (id,user_low,user_high,initiator_id) VALUES ('0198f222-0000-7000-8000-000000000001',$1,$2,$1)", u1, u2); err != nil {
		t.Fatal(err)
	}
	// 4. message idempotency key
	ins := "INSERT INTO messages (id,conversation_id,seq,sender_id,client_id,body) VALUES (gen_random_uuid(),'0198f222-0000-7000-8000-000000000001',$1,$2,'0198f333-0000-7000-8000-000000000001','hi')"
	if err := exec(t, pool, ins, 1, u1); err != nil {
		t.Fatal(err)
	}
	if err := exec(t, pool, ins, 2, u1); err == nil {
		t.Error("same client_id twice must fail")
	}
	// 5. one open report per reporter+target
	rep := "INSERT INTO reports (id,reporter_id,target_type,target_id,reason) VALUES (gen_random_uuid(),$1,'idea','0198f444-0000-7000-8000-000000000001','spam')"
	if err := exec(t, pool, rep, u1); err != nil {
		t.Fatal(err)
	}
	if err := exec(t, pool, rep, u1); err == nil {
		t.Error("duplicate open report must fail")
	}
	// 8. published_rev <= rev
	if err := exec(t, pool, "INSERT INTO articles (id,category_id,source_locale,rev,published_rev) VALUES (gen_random_uuid(),'0198f000-0000-7000-8000-000000000001','bg',1,2)"); err == nil {
		t.Error("published_rev > rev must fail")
	}
	// seed
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM categories").Scan(&n); err != nil || n != 3 {
		t.Errorf("seed categories: %d %v", n, err)
	}
	// 7. no body column in article tables
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM information_schema.columns WHERE table_name IN ('articles','article_revisions','article_locales') AND column_name LIKE '%body%'").Scan(&n); err != nil || n != 0 {
		t.Errorf("article tables must not store body: %d", n)
	}
}
