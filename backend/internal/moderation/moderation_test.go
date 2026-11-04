package moderation_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"beeloy.com/backend/internal/acl"
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
	env                                  *servertest.Env
	reporter, offender, mod, admin, anon *servertest.Client
	offID                                string
}

func newWorld(t *testing.T) *world {
	env := servertest.New(t)
	w := &world{env: env, anon: env.Client(), reporter: env.LoginAs("rep@example.com"), offender: env.LoginAs("off@example.com"), mod: env.LoginAs("mod@example.com"), admin: env.LoginAs("admin@example.com")}
	w.offID = w.offender.UserID()
	env.Grant(w.mod.UserID(), acl.RoleModerator, nil)
	env.Grant(w.admin.UserID(), acl.RoleAdmin, nil)
	env.Server.Pool.Exec(context.Background(), `UPDATE users SET created_at = now() - interval '3 days'`)
	return w
}

func send(c *servertest.Client, conv, body string) *servertest.Resp {
	return c.Post("/api/v1/conversations/"+conv+"/messages", map[string]any{"client_id": uuid.NewString(), "body": body})
}

func TestMessageReportSnapshotAndAudit(t *testing.T) {
	w := newWorld(t)
	conv := w.offender.Post("/api/v1/conversations", map[string]any{"user_id": w.reporter.UserID()}).Data()["id"].(string)
	var bad string
	for i := 1; i <= 8; i++ {
		r := send(w.offender, conv, fmt.Sprintf("line %d", i))
		if i == 3 {
			w.reporter.Post("/api/v1/conversations/"+conv+"/accept", nil)
			r = send(w.offender, conv, "line 3b")
		}
		if i == 6 && r.Code == 201 {
			bad = r.Data()["id"].(string)
		}
	}
	if bad == "" {
		t.Fatal("no target message")
	}
	r := w.reporter.Post("/api/v1/reports", map[string]any{"target_type": "message", "target_id": bad, "reason": "abuse", "details": "insulting"})
	if r.Code != 201 {
		t.Fatalf("report: %d %s", r.Code, r.Raw)
	}
	if strings.Contains(string(r.Raw), "line ") || r.Data()["reporter_id"] != nil {
		t.Errorf("reporter response must not echo snapshot or reporter id: %s", r.Raw)
	}
	rid := r.Data()["id"].(string)
	if again := w.reporter.Post("/api/v1/reports", map[string]any{"target_type": "message", "target_id": bad, "reason": "abuse"}); again.Code != 200 || again.Data()["id"] != rid {
		t.Errorf("duplicate open report is idempotent: %d", again.Code)
	}
	// moderator: list has no snapshot, the single view has <= 5 messages and is audited
	l := w.mod.Get("/api/v1/mod/reports")
	if len(l.List()) != 1 || strings.Contains(string(l.Raw), "line ") {
		t.Fatalf("list: %s", l.Raw)
	}
	var before int
	w.env.Server.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='report.view_snapshot'`).Scan(&before)
	if before != 0 {
		t.Fatal("listing must not be audited as a snapshot view")
	}
	g := w.mod.Get("/api/v1/mod/reports/" + rid)
	snap, _ := g.Data()["snapshot"].([]any)
	if g.Code != 200 || len(snap) == 0 || len(snap) > 5 {
		t.Fatalf("snapshot: %d %s", g.Code, g.Raw)
	}
	var after int
	w.env.Server.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='report.view_snapshot'`).Scan(&after)
	if after != 1 {
		t.Errorf("snapshot view must be audited: %d", after)
	}
	// members cannot read reports; offender cannot be reported by a stranger via message
	if w.reporter.Get("/api/v1/mod/reports").Code != 403 || w.anon.Get("/api/v1/mod/reports").Code != 401 {
		t.Error("report access")
	}
	stranger := w.env.LoginAs("stranger@example.com")
	if rr := stranger.Post("/api/v1/reports", map[string]any{"target_type": "message", "target_id": bad, "reason": "spam"}); rr.Code != 404 {
		t.Errorf("stranger reporting a private message: %d", rr.Code)
	}
	if rr := w.offender.Post("/api/v1/reports", map[string]any{"target_type": "message", "target_id": bad, "reason": "spam"}); rr.Code != 422 {
		t.Errorf("reporting own message: %d", rr.Code)
	}
	// resolve once
	if rr := w.mod.Post("/api/v1/mod/reports/"+rid+"/resolve", map[string]any{"resolution": "resolved", "note": "warned"}); rr.Code != 200 || rr.Data()["status"] != "resolved" {
		t.Fatalf("resolve: %d %s", rr.Code, rr.Raw)
	}
	if rr := w.mod.Post("/api/v1/mod/reports/"+rid+"/resolve", map[string]any{"resolution": "dismissed"}); rr.Code != 409 {
		t.Errorf("second resolve: %d", rr.Code)
	}
	if len(w.mod.Get("/api/v1/mod/reports").List()) != 0 || len(w.mod.Get("/api/v1/mod/reports?status=resolved").List()) != 1 {
		t.Error("status filter")
	}
}

func TestReportValidationAndTargets(t *testing.T) {
	w := newWorld(t)
	idea := w.offender.Post("/api/v1/ideas", map[string]any{"title": "Reportable idea", "body": strings.Repeat("z", 30), "category": "other", "locale": "en"}).Data()["id"].(string)
	for name, body := range map[string]map[string]any{
		"idea": {"target_type": "idea", "target_id": idea, "reason": "spam"},
		"user": {"target_type": "user", "target_id": w.offID, "reason": "abuse", "details": "x"},
	} {
		if r := w.reporter.Post("/api/v1/reports", body); r.Code != 201 {
			t.Errorf("%s: %d %s", name, r.Code, r.Raw)
		}
	}
	cm := w.offender.Post("/api/v1/comments", map[string]any{"target_type": "idea", "target_id": idea, "body": "bad comment"}).Data()["id"].(string)
	if r := w.reporter.Post("/api/v1/reports", map[string]any{"target_type": "comment", "target_id": cm, "reason": "abuse"}); r.Code != 201 {
		t.Errorf("comment report: %d %s", r.Code, r.Raw)
	}
	if r := w.reporter.Post("/api/v1/reports", map[string]any{"target_type": "idea", "target_id": idea, "reason": "boring"}); r.Code != 422 {
		t.Errorf("bad reason: %d", r.Code)
	}
	if r := w.reporter.Post("/api/v1/reports", map[string]any{"target_type": "idea", "target_id": uuid.NewString(), "reason": "spam"}); r.Code != 404 {
		t.Errorf("unknown target: %d", r.Code)
	}
	if r := w.reporter.Post("/api/v1/reports", map[string]any{"target_type": "user", "target_id": idea, "reason": "spam"}); r.Code != 404 {
		t.Errorf("type mismatch: %d", r.Code)
	}
	// a hidden comment can no longer be reported
	w.mod.Post("/api/v1/mod/comments/"+cm+"/hide", nil)
	other := w.env.LoginAs("o2@example.com")
	if r := other.Post("/api/v1/reports", map[string]any{"target_type": "comment", "target_id": cm, "reason": "abuse"}); r.Code != 404 {
		t.Errorf("hidden comment: %d", r.Code)
	}
}

func TestBlockAccount(t *testing.T) {
	w := newWorld(t)
	if r := w.mod.Post("/api/v1/mod/users/"+w.offID+"/block", map[string]any{"reason": "no"}); r.Code != 422 {
		t.Errorf("reason required: %d", r.Code)
	}
	if r := w.reporter.Post("/api/v1/mod/users/"+w.offID+"/block", map[string]any{"reason": "because I want"}); r.Code != 403 {
		t.Errorf("member blocks: %d", r.Code)
	}
	if r := w.mod.Post("/api/v1/mod/users/"+w.admin.UserID()+"/block", map[string]any{"reason": "coup attempt"}); r.Code != 403 {
		t.Errorf("admins cannot be blocked: %d", r.Code)
	}
	if r := w.mod.Post("/api/v1/mod/users/"+w.mod.UserID()+"/block", map[string]any{"reason": "self harm"}); r.Code != 403 {
		t.Errorf("self block: %d", r.Code)
	}
	if w.offender.Get("/api/v1/me").Code != 200 {
		t.Fatal("precondition")
	}
	if r := w.mod.Post("/api/v1/mod/users/"+w.offID+"/block", map[string]any{"reason": "repeated abuse"}); r.Code != 204 {
		t.Fatalf("block: %d %s", r.Code, r.Raw)
	}
	if w.offender.Get("/api/v1/me").Code != 401 {
		t.Error("the old session must stop working immediately")
	}
	var st string
	w.env.Server.Pool.QueryRow(context.Background(), `SELECT status FROM resources WHERE id=$1`, w.offID).Scan(&st)
	if st != "hidden" {
		t.Errorf("registry: %s", st)
	}
	if r := w.mod.Delete("/api/v1/mod/users/" + w.offID + "/block"); r.Code != 204 {
		t.Fatalf("unblock: %d", r.Code)
	}
	if r := w.mod.Delete("/api/v1/mod/users/" + w.offID + "/block"); r.Code != 409 {
		t.Errorf("unblock twice: %d", r.Code)
	}
	if w.env.LoginAs("off@example.com").Get("/api/v1/me").Code != 200 {
		t.Error("user can sign in again after unblock")
	}
}
