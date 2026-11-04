package privacy_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/platform/s3store"
	"beeloy.com/backend/internal/privacy"
	"beeloy.com/backend/internal/server/servertest"
	"beeloy.com/backend/internal/testutil"
	"github.com/google/uuid"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

func TestDeletionLifecycleAndPurge(t *testing.T) {
	env := servertest.New(t)
	ctx := context.Background()
	user := env.SignInSlug("leaver@example.com", "leaver", "Leaver")
	friend := env.SignInSlug("friend@example.com", "friend", "Friend")
	uid, fid := user.UserID(), friend.UserID()
	env.Server.Pool.Exec(ctx, `UPDATE users SET created_at = now() - interval '3 days'`)

	// content of the leaving user
	idea := user.Post("/api/v1/ideas", map[string]any{"title": "My farewell idea", "body": strings.Repeat("idea text ", 5), "category": "other", "locale": "en"}).Data()["id"].(string)
	friendIdea := friend.Post("/api/v1/ideas", map[string]any{"title": "Friend idea here", "body": strings.Repeat("friend idea ", 5), "category": "other", "locale": "en"}).Data()["id"].(string)
	user.Put("/api/v1/ideas/"+friendIdea+"/vote", nil)
	user.Post("/api/v1/comments", map[string]any{"target_type": "idea", "target_id": friendIdea, "body": "my personal comment"})
	conv := user.Post("/api/v1/conversations", map[string]any{"user_id": fid}).Data()["id"].(string)
	user.Post("/api/v1/conversations/"+conv+"/messages", map[string]any{"client_id": uuid.NewString(), "body": "secret hello"})
	friend.Post("/api/v1/conversations/"+conv+"/accept", nil)
	friend.Post("/api/v1/conversations/"+conv+"/messages", map[string]any{"client_id": uuid.NewString(), "body": "friend reply"})

	// request: pending, still signed in, restricted
	if r := user.Delete("/api/v1/me"); r.Code != 202 || r.Data()["status"] != "pending_deletion" {
		t.Fatalf("request: %d %s", r.Code, r.Raw)
	}
	if user.Delete("/api/v1/me").Code != 202 {
		t.Error("idempotent")
	}
	if user.Get("/api/v1/me").Code != 200 || user.Patch("/api/v1/me", map[string]any{"bio": "x"}).Code != 403 {
		t.Error("pending: read-only access")
	}
	if user.Post("/api/v1/ideas", map[string]any{"title": "Late idea", "body": strings.Repeat("late ", 10), "category": "other", "locale": "en"}).Code != 403 {
		t.Error("pending accounts cannot post")
	}
	var reg string
	env.Server.Pool.QueryRow(ctx, `SELECT status FROM resources WHERE id=$1`, uid).Scan(&reg)
	if reg != "hidden" {
		t.Errorf("registry status: %s", reg)
	}
	// restore, then request again
	if r := user.Post("/api/v1/me/restore", nil); r.Code != 200 || r.Data()["status"] != "active" {
		t.Fatalf("restore: %d %s", r.Code, r.Raw)
	}
	if user.Post("/api/v1/me/restore", nil).Code != 409 {
		t.Error("restore of an active account")
	}
	user.Delete("/api/v1/me")

	// the job purges only accounts whose grace period is over
	if ran, err := env.Server.Jobs.RunTask(ctx, "PurgeDeletedAccounts"); err != nil || !ran {
		t.Fatalf("job: %v %v", ran, err)
	}
	var st string
	env.Server.Pool.QueryRow(ctx, `SELECT status FROM users WHERE id=$1`, uid).Scan(&st)
	if st != "pending_deletion" {
		t.Fatalf("within the grace period nothing is purged: %s", st)
	}
	env.Server.Pool.Exec(ctx, `UPDATE users SET deletion_requested_at = now() - interval '31 days' WHERE id=$1`, uid)
	if _, err := env.Server.Jobs.RunTask(ctx, "PurgeDeletedAccounts"); err != nil {
		t.Fatal(err)
	}
	var nick, bio string
	var pp bool
	env.Server.Pool.QueryRow(ctx, `SELECT status, nickname, bio, profile_public FROM users WHERE id=$1`, uid).Scan(&st, &nick, &bio, &pp)
	if st != "deleted" || nick != "Deleted user" || bio != "" || pp {
		t.Fatalf("anonymized user: %s %s %q %v", st, nick, bio, pp)
	}
	count := func(sql string, args ...any) int {
		var n int
		env.Server.Pool.QueryRow(ctx, sql, args...).Scan(&n)
		return n
	}
	for name, sql := range map[string]string{
		"identities": `SELECT count(*) FROM auth_identities WHERE user_id=$1`,
		"sessions":   `SELECT count(*) FROM sessions WHERE user_id=$1`,
		"slugs":      `SELECT count(*) FROM slugs WHERE entity_type='user' AND entity_id=$1`,
		"votes":      `SELECT count(*) FROM idea_votes WHERE user_id=$1`,
		"roles":      `SELECT count(*) FROM role_bindings WHERE user_id=$1`,
	} {
		if n := count(sql, uid); n != 0 {
			t.Errorf("%s left: %d", name, n)
		}
	}
	if n := count(`SELECT count(*) FROM messages WHERE sender_id=$1 AND body <> ''`, uid); n != 0 {
		t.Error("message bodies must be wiped")
	}
	if n := count(`SELECT count(*) FROM messages WHERE conversation_id=$1`, conv); n != 2 {
		t.Errorf("message rows stay for the other side: %d", n)
	}
	if n := count(`SELECT votes_count FROM ideas WHERE id=$1`, friendIdea); n != 0 {
		t.Errorf("vote count must drop: %d", n)
	}
	if n := count(`SELECT count(*) FROM ideas WHERE id=$1 AND author_id IS NULL`, idea); n != 1 {
		t.Error("idea must stay, without an author")
	}
	if n := count(`SELECT count(*) FROM comments WHERE status='deleted' AND body='' AND author_id IS NULL`); n != 1 {
		t.Error("comments are wiped")
	}
	// the friend sees "message deleted" and the slug becomes free
	hist := friend.Get("/api/v1/conversations/" + conv + "/messages").List()
	if len(hist) != 2 || hist[0].(map[string]any)["body"] != "" || hist[0].(map[string]any)["deleted"] != true || hist[1].(map[string]any)["body"] != "friend reply" {
		t.Errorf("history for the other side: %v", hist)
	}
	if ok, _ := env.Server.Slugs.IsAvailable(ctx, env.Server.Pool, "user", "leaver"); !ok {
		t.Error("the slug must be free again")
	}
	if env.Client().Get("/api/v1/users/leaver").Code != 404 {
		t.Error("profile gone")
	}
	if user.Get("/api/v1/me").Code != 401 {
		t.Error("sessions gone")
	}
	if c := friend.Get("/api/v1/ideas/friend-idea-here").Data()["idea"].(map[string]any); c["votes_count"].(float64) != 0 {
		t.Error("public vote counter")
	}
	_ = time.Now
}

func TestExportContainsOnlyOwnData(t *testing.T) {
	env := servertest.New(t)
	a := env.SignInSlug("a@example.com", "alice-x", "Alice")
	b := env.SignInSlug("b@example.com", "bob-x", "Bob")
	env.Server.Pool.Exec(context.Background(), `UPDATE users SET created_at = now() - interval '3 days'`)
	conv := a.Post("/api/v1/conversations", map[string]any{"user_id": b.UserID()}).Data()["id"].(string)
	a.Post("/api/v1/conversations/"+conv+"/messages", map[string]any{"client_id": uuid.NewString(), "body": "hello bob"})
	b.Post("/api/v1/ideas", map[string]any{"title": "Bob private-ish idea", "body": strings.Repeat("bob idea ", 5), "category": "other", "locale": "en"})
	a.Post("/api/v1/ideas", map[string]any{"title": "Alice own idea", "body": strings.Repeat("alice idea ", 5), "category": "other", "locale": "en"})

	r := a.Get("/api/v1/me/export")
	if r.Code != 200 || !strings.Contains(r.Header.Get("Content-Disposition"), "beeloy-export.json") {
		t.Fatalf("export: %d %v", r.Code, r.Header)
	}
	var doc map[string]any
	if err := json.Unmarshal(r.Raw, &doc); err != nil {
		t.Fatal(err)
	}
	s := string(r.Raw)
	if !strings.Contains(s, "hello bob") || !strings.Contains(s, "Alice own idea") || !strings.Contains(s, "a@example.com") {
		t.Error("own data must be present")
	}
	if strings.Contains(s, "Bob private-ish idea") || strings.Contains(s, "b@example.com") {
		t.Error("other people's data must not leak")
	}
	if len(doc["conversations"].([]any)) != 1 || doc["conversations"].([]any)[0].(map[string]any)["peer_slug"] != "bob-x" {
		t.Errorf("conversations: %v", doc["conversations"])
	}
	for i := 0; i < 2; i++ {
		a.Get("/api/v1/me/export")
	}
	if a.Get("/api/v1/me/export").Code != 429 {
		t.Error("export is limited to 3 per day")
	}
	if env.Client().Get("/api/v1/me/export").Code != 401 {
		t.Error("anonymous export")
	}
	var n int
	env.Server.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='user.export'`).Scan(&n)
	if n != 3 {
		t.Errorf("exports must be audited: %d", n)
	}
	_ = acl.RoleAdmin
}

// A purge is journaled outside PostgreSQL; replay re-applies it to a database restored from an older backup.
func TestDeletionJournalReplay(t *testing.T) {
	env := servertest.New(t)
	ctx := context.Background()
	store := s3store.NewMem()
	j, err := privacy.NewS3Journal(store, strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	env.Server.Privacy.Journal = j
	user := env.SignInSlug("gone@example.com", "gone", "Gone")
	uid := uuid.MustParse(user.UserID())
	env.Server.Pool.Exec(ctx, `UPDATE users SET created_at = now() - interval '3 days'`)
	if err := env.Server.Privacy.PurgeUser(ctx, uid); err != nil {
		t.Fatal(err)
	}
	entries, err := j.List(ctx)
	if err != nil || len(entries) != 1 || entries[0].UserID != uid {
		t.Fatalf("journal: %v %v", entries, err)
	}
	// the stored object must not contain the plain user id
	_ = store.List(ctx, "deletion-journal/", func(o s3store.Object) error {
		b, _ := store.Get(ctx, o.Key, 4096)
		if strings.Contains(string(b), uid.String()) {
			t.Error("journal entry is not encrypted")
		}
		return nil
	})
	// wrong key cannot read it
	other, _ := privacy.NewS3Journal(store, strings.Repeat("cd", 32))
	if _, err := other.List(ctx); err == nil {
		t.Error("wrong key must fail")
	}
	// simulate restoring an older backup in which the account is still alive
	env.Server.Pool.Exec(ctx, `UPDATE users SET status='active', nickname='Gone' WHERE id=$1`, uid)
	n, err := env.Server.Privacy.ReplayDeletions(ctx)
	if err != nil || n != 1 {
		t.Fatalf("replay: %d %v", n, err)
	}
	var st, nick string
	env.Server.Pool.QueryRow(ctx, `SELECT status, nickname FROM users WHERE id=$1`, uid).Scan(&st, &nick)
	if st != "deleted" || nick != "Deleted user" {
		t.Errorf("after replay: %s %s", st, nick)
	}
	if n, err := env.Server.Privacy.ReplayDeletions(ctx); err != nil || n != 0 {
		t.Errorf("second replay must be a no-op: %d %v", n, err)
	}
}
