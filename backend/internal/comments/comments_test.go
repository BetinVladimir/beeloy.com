package comments_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"beeloy.com/backend/internal/acl"
	"beeloy.com/backend/internal/server/servertest"
	"beeloy.com/backend/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

type world struct {
	env                     *servertest.Env
	author, user, mod, anon *servertest.Client
	ideaID, ideaSlug        string
}

func newWorld(t *testing.T) *world {
	env := servertest.New(t)
	w := &world{env: env, anon: env.Client(), author: env.LoginAs("author@example.com"), user: env.SignInSlug("business@example.com", "business-expert", "Business Expert"), mod: env.LoginAs("mod@example.com")}
	env.Grant(w.mod.UserID(), acl.RoleModerator, nil)
	r := w.author.Post("/api/v1/ideas", map[string]any{"title": "Idea to discuss", "body": strings.Repeat("Discuss me. ", 5), "category": "product", "locale": "en"})
	w.ideaID, w.ideaSlug = r.Data()["id"].(string), r.Data()["slug"].(string)
	return w
}

func (w *world) post(c *servertest.Client, body string, parent any) *servertest.Resp {
	req := map[string]any{"target_type": "idea", "target_id": w.ideaID, "body": body}
	if parent != nil {
		req["parent_id"] = parent
	}
	return c.Post("/api/v1/comments", req)
}

func TestCommentShowsNicknameAndSlug(t *testing.T) {
	w := newWorld(t)
	r := w.post(w.user, "We automated order processing, @business-expert style.", nil)
	if r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Raw)
	}
	a := r.Data()["author"].(map[string]any)
	if a["nickname"] != "Business Expert" || a["slug"] != "business-expert" {
		t.Fatalf("author: %v", a)
	}
	list := w.anon.Get("/api/v1/comments?target_type=idea&target_id=" + w.ideaID)
	if list.Code != 200 || len(list.List()) != 1 || list.List()[0].(map[string]any)["author"].(map[string]any)["slug"] != "business-expert" {
		t.Fatalf("public list: %d %s", list.Code, list.Raw)
	}
	// replies: one level only
	parent := r.Data()["id"].(string)
	rep := w.post(w.author, "Thanks for sharing", parent)
	if rep.Code != 201 {
		t.Fatalf("reply: %d %s", rep.Code, rep.Raw)
	}
	if r2 := w.post(w.user, "reply to reply", rep.Data()["id"]); r2.Code != 409 {
		t.Errorf("reply to a reply: %d", r2.Code)
	}
	top := w.anon.Get("/api/v1/comments?target_type=idea&target_id=" + w.ideaID).List()
	if len(top) != 1 || top[0].(map[string]any)["reply_count"].(float64) != 1 {
		t.Fatalf("reply_count: %v", top)
	}
	if rl := w.anon.Get("/api/v1/comments/" + parent + "/replies").List(); len(rl) != 1 {
		t.Errorf("replies: %v", rl)
	}
	// profile activity
	pa := w.anon.Get("/api/v1/users/business-expert/comments").List()
	if len(pa) != 1 || pa[0].(map[string]any)["target_slug"] != w.ideaSlug {
		t.Fatalf("profile comments: %v", pa)
	}
}

func TestPermissionsAndValidation(t *testing.T) {
	w := newWorld(t)
	if r := w.post(w.anon, "hello there", nil); r.Code != 401 {
		t.Errorf("anon: %d", r.Code)
	}
	if r := w.post(w.user, "   ", nil); r.Code != 422 {
		t.Errorf("empty: %d", r.Code)
	}
	if r := w.post(w.user, strings.Repeat("x", 2001), nil); r.Code != 422 {
		t.Errorf("too long: %d", r.Code)
	}
	if r := w.user.Post("/api/v1/comments", map[string]any{"target_type": "user", "target_id": w.ideaID, "body": "x y"}); r.Code != 422 {
		t.Errorf("bad target type: %d", r.Code)
	}
	if r := w.user.Post("/api/v1/comments", map[string]any{"target_type": "idea", "target_id": "0198f111-0000-7000-8000-000000000009", "body": "hi hi"}); r.Code != 404 {
		t.Errorf("unknown target: %d", r.Code)
	}
	// a new account may post at most one link
	if r := w.post(w.user, "see https://a.example and https://b.example", nil); r.Code != 422 {
		t.Errorf("two links from a new account: %d %s", r.Code, r.Raw)
	}
	if r := w.post(w.user, "see https://a.example", nil); r.Code != 201 {
		t.Errorf("one link: %d", r.Code)
	}
	// private article is not commentable
	ed := w.env.LoginAs("ed@example.com")
	w.env.Grant(ed.UserID(), acl.RoleEditor, nil)
	art := ed.Post("/api/v1/editor/articles", map[string]any{"category_id": servertest.CatNews, "source_locale": "en", "title_for_slug": "Hidden draft"})
	if r := w.user.Post("/api/v1/comments", map[string]any{"target_type": "article", "target_id": art.Data()["id"], "body": "peek peek"}); r.Code != 404 {
		t.Errorf("comment on a draft: %d", r.Code)
	}
}

func TestRateLimitPerMinute(t *testing.T) {
	w := newWorld(t)
	for i := 0; i < 10; i++ {
		if r := w.post(w.user, fmt.Sprintf("comment number %d", i), nil); r.Code != 201 {
			t.Fatalf("comment %d: %d %s", i, r.Code, r.Raw)
		}
	}
	if r := w.post(w.user, "one more", nil); r.Code != 429 {
		t.Errorf("11th comment per minute: %d", r.Code)
	}
}

func TestDeleteHideAndPlaceholders(t *testing.T) {
	w := newWorld(t)
	id := w.post(w.user, "to be hidden by a moderator", nil).Data()["id"].(string)
	child := w.post(w.author, "a reply keeps the thread", id).Data()["id"].(string)
	other := w.post(w.author, "mine", nil).Data()["id"].(string)
	if r := w.author.Delete("/api/v1/comments/" + id); r.Code != 403 {
		t.Errorf("delete foreign comment: %d", r.Code)
	}
	if r := w.user.Post("/api/v1/mod/comments/"+id+"/hide", nil); r.Code != 403 {
		t.Errorf("member hide: %d", r.Code)
	}
	if r := w.mod.Post("/api/v1/mod/comments/"+id+"/hide", nil); r.Code != 204 {
		t.Fatalf("hide: %d", r.Code)
	}
	var found map[string]any
	for _, c := range w.anon.Get("/api/v1/comments?target_type=idea&target_id=" + w.ideaID).List() {
		if c.(map[string]any)["id"] == id {
			found = c.(map[string]any)
		}
	}
	if found == nil || found["body"] != "" || found["status"] != "hidden" || found["reply_count"].(float64) != 1 {
		t.Fatalf("hidden comment must stay as an empty placeholder: %v", found)
	}
	if pa := w.anon.Get("/api/v1/users/business-expert/comments").List(); len(pa) != 0 {
		t.Errorf("hidden comments are not in profile activity: %v", pa)
	}
	if r := w.mod.Post("/api/v1/mod/comments/"+id+"/unhide", nil); r.Code != 204 {
		t.Errorf("unhide: %d", r.Code)
	}
	// author deletes own comment: body wiped; a comment without replies disappears from lists
	if r := w.author.Delete("/api/v1/comments/" + other); r.Code != 204 {
		t.Fatalf("own delete: %d", r.Code)
	}
	for _, c := range w.anon.Get("/api/v1/comments?target_type=idea&target_id=" + w.ideaID).List() {
		if c.(map[string]any)["id"] == other {
			t.Error("deleted comment without replies must not be listed")
		}
	}
	var body string
	w.env.Server.Pool.QueryRow(t.Context(), `SELECT body FROM comments WHERE id=$1`, other).Scan(&body)
	if body != "" {
		t.Error("deleted body must be wiped")
	}
	// registry follows the status
	var st string
	w.env.Server.Pool.QueryRow(t.Context(), `SELECT status FROM resources WHERE id=$1`, other).Scan(&st)
	if st != "removed" {
		t.Errorf("registry status: %s", st)
	}
	var n int
	w.env.Server.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action IN ('comment.hide','comment.unhide')`).Scan(&n)
	if n != 2 {
		t.Errorf("audit: %d", n)
	}
	_ = child
	// canonical link by UUID points to the idea page with an anchor
	rr := w.anon.Get("/api/v1/resources/" + child)
	if rr.Code != 200 || rr.Data()["canonical_path"] != "/ideas/"+w.ideaSlug+"#comment-"+child {
		t.Fatalf("resource link: %d %s", rr.Code, rr.Raw)
	}
}
