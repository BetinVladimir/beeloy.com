package messenger_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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
	env           *servertest.Env
	a, b, c       *servertest.Client
	aID, bID, cID string
}

func newWorld(t *testing.T) *world {
	env := servertest.New(t)
	w := &world{env: env, a: env.LoginAs("a@example.com"), b: env.LoginAs("b@example.com"), c: env.LoginAs("c@example.com")}
	w.aID, w.bID, w.cID = w.a.UserID(), w.b.UserID(), w.c.UserID()
	// age the accounts so that the new-account restrictions do not interfere (they have their own tests)
	env.Server.Pool.Exec(context.Background(), `UPDATE users SET created_at = now() - interval '3 days'`)
	return w
}

func (w *world) start(t *testing.T, from *servertest.Client, to string) string {
	r := from.Post("/api/v1/conversations", map[string]any{"user_id": to})
	if r.Code != 201 && r.Code != 200 {
		t.Fatalf("start: %d %s", r.Code, r.Raw)
	}
	return r.Data()["id"].(string)
}

func send(c *servertest.Client, conv, body string) *servertest.Resp {
	return c.Post("/api/v1/conversations/"+conv+"/messages", map[string]any{"client_id": uuid.NewString(), "body": body})
}

func TestMessageRequestFlow(t *testing.T) {
	w := newWorld(t)
	// anyone can write to anyone: no follow or permission needed
	r := w.a.Post("/api/v1/conversations", map[string]any{"user_id": w.bID})
	if r.Code != 201 || r.Data()["status"] != "request" || r.Data()["initiated_by_me"] != true {
		t.Fatalf("start: %d %s", r.Code, r.Raw)
	}
	conv := r.Data()["id"].(string)
	if w.a.Post("/api/v1/conversations", map[string]any{"user_id": w.bID}).Code != 200 {
		t.Error("starting again returns the existing dialog")
	}
	if w.b.Post("/api/v1/conversations", map[string]any{"user_id": w.aID}).Code != 200 {
		t.Error("the other side sees the same dialog")
	}
	for i := 1; i <= 3; i++ {
		if r := send(w.a, conv, fmt.Sprintf("hello %d", i)); r.Code != 201 || r.Data()["seq"].(float64) != float64(i) {
			t.Fatalf("message %d: %d %s", i, r.Code, r.Raw)
		}
	}
	if r := send(w.a, conv, "hello 4"); r.Code != 409 {
		t.Fatalf("4th message before an answer: %d %s", r.Code, r.Raw)
	}
	// recipient: request folder, not inbox, not in unread total
	if n := len(w.b.Get("/api/v1/conversations").List()); n != 0 {
		t.Errorf("inbox must not contain requests: %d", n)
	}
	if rq := w.b.Get("/api/v1/conversations?folder=requests").List(); len(rq) != 1 || rq[0].(map[string]any)["last_message_preview"] != "hello 3" {
		t.Fatalf("requests: %v", rq)
	}
	if c := w.b.Get("/api/v1/conversations/unread-count").Data(); c["total"].(float64) != 0 || c["requests"].(float64) != 1 {
		t.Errorf("counters: %v", c)
	}
	if n := len(w.a.Get("/api/v1/conversations?folder=sent").List()); n != 1 {
		t.Errorf("sent: %d", n)
	}
	// initiator cannot accept; recipient accepts
	if w.a.Post("/api/v1/conversations/"+conv+"/accept", nil).Code != 403 {
		t.Error("initiator must not accept")
	}
	if w.b.Post("/api/v1/conversations/"+conv+"/accept", nil).Code != 204 {
		t.Fatal("accept")
	}
	if send(w.a, conv, "now unlimited").Code != 201 {
		t.Error("after accept the initiator can write freely")
	}
	if c := w.b.Get("/api/v1/conversations/unread-count").Data(); c["total"].(float64) != 4 || c["requests"].(float64) != 0 {
		t.Errorf("counters after accept: %v", c)
	}
	if len(w.b.Get("/api/v1/conversations").List()) != 1 {
		t.Error("accepted dialog moves to the inbox")
	}
}

func TestDeclineAndAnswerAccepts(t *testing.T) {
	w := newWorld(t)
	conv := w.start(t, w.a, w.bID)
	send(w.a, conv, "spam?")
	if w.b.Post("/api/v1/conversations/"+conv+"/decline", nil).Code != 204 {
		t.Fatal("decline")
	}
	if r := send(w.a, conv, "please read me"); r.Code != 403 || !strings.Contains(string(r.Raw), "conversation_declined") {
		t.Fatalf("declined initiator: %d %s", r.Code, r.Raw)
	}
	// the recipient may still change their mind by writing
	if r := send(w.b, conv, "ok, I am listening"); r.Code != 201 {
		t.Fatalf("recipient writes into declined dialog: %d %s", r.Code, r.Raw)
	}
	if send(w.a, conv, "thanks").Code != 201 {
		t.Error("dialog is accepted after the recipient's answer")
	}
	// a plain reply accepts a fresh request
	conv2 := w.start(t, w.a, w.cID)
	send(w.a, conv2, "hi c")
	if send(w.c, conv2, "hi a").Code != 201 || len(w.c.Get("/api/v1/conversations").List()) != 1 {
		t.Error("an answer accepts the request")
	}
}

func TestIdempotentAndConcurrentSend(t *testing.T) {
	w := newWorld(t)
	conv := w.start(t, w.a, w.bID)
	w.b.Post("/api/v1/conversations/"+conv+"/accept", nil)
	cid := uuid.NewString()
	body := map[string]any{"client_id": cid, "body": "exactly once"}
	r1 := w.a.Post("/api/v1/conversations/"+conv+"/messages", body)
	r2 := w.a.Post("/api/v1/conversations/"+conv+"/messages", body)
	if r1.Code != 201 || r2.Code != 200 || r1.Data()["id"] != r2.Data()["id"] || r1.Data()["seq"] != r2.Data()["seq"] {
		t.Fatalf("idempotency: %d/%d %s %s", r1.Code, r2.Code, r1.Raw, r2.Raw)
	}
	// 20 parallel sends: seq without gaps or duplicates
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := w.env.Client()
			c.Cookies = w.a.Cookies
			if r := send(c, conv, fmt.Sprintf("parallel %d", i)); r.Code != 201 {
				t.Errorf("parallel %d: %d %s", i, r.Code, r.Raw)
			}
		}(i)
	}
	wg.Wait()
	h := w.b.Get("/api/v1/conversations/" + conv + "/messages?limit=100")
	msgs := h.List()
	if len(msgs) != 21 {
		t.Fatalf("messages: %d", len(msgs))
	}
	for i, m := range msgs {
		if m.(map[string]any)["seq"].(float64) != float64(i+1) {
			t.Fatalf("seq gap at %d: %v", i, m)
		}
	}
	// paging back and catching up
	if p := w.b.Get("/api/v1/conversations/" + conv + "/messages?before_seq=10&limit=3").List(); len(p) != 3 || p[0].(map[string]any)["seq"].(float64) != 7 || p[2].(map[string]any)["seq"].(float64) != 9 {
		t.Errorf("before_seq page: %v", p)
	}
	if p := w.b.Get("/api/v1/conversations/" + conv + "/messages?after_seq=18").List(); len(p) != 3 {
		t.Errorf("after_seq catch-up: %v", p)
	}
}

func TestIsolationBetweenConversations(t *testing.T) {
	w := newWorld(t)
	conv := w.start(t, w.a, w.bID)
	send(w.a, conv, "private text")
	for name, r := range map[string]*servertest.Resp{
		"read":  w.c.Get("/api/v1/conversations/" + conv + "/messages"),
		"send":  send(w.c, conv, "intruder"),
		"mark":  w.c.Post("/api/v1/conversations/"+conv+"/read", map[string]any{"seq": 1}),
		"mute":  w.c.Put("/api/v1/conversations/"+conv+"/mute", nil),
		"accpt": w.c.Post("/api/v1/conversations/"+conv+"/accept", nil),
	} {
		if r.Code != 404 {
			t.Errorf("%s by a stranger: %d", name, r.Code)
		}
	}
	if w.env.Client().Get("/api/v1/conversations").Code != 401 {
		t.Error("anonymous")
	}
	if len(w.c.Get("/api/v1/conversations?folder=requests").List()) != 0 {
		t.Error("stranger sees nothing")
	}
	if r := w.a.Post("/api/v1/conversations", map[string]any{"user_id": w.aID}); r.Code != 422 {
		t.Errorf("dialog with oneself: %d", r.Code)
	}
	if r := w.a.Post("/api/v1/conversations", map[string]any{"user_id": uuid.NewString()}); r.Code != 422 {
		t.Errorf("unknown user: %d", r.Code)
	}
}

func TestBlocking(t *testing.T) {
	w := newWorld(t)
	conv := w.start(t, w.a, w.bID)
	w.b.Post("/api/v1/conversations/"+conv+"/accept", nil)
	send(w.a, conv, "before block")
	if w.b.Put("/api/v1/users/"+w.aID+"/block", nil).Code != 204 || w.b.Put("/api/v1/users/"+w.aID+"/block", nil).Code != 204 {
		t.Fatal("block must be idempotent")
	}
	if r := send(w.a, conv, "after block"); r.Code != 403 {
		t.Errorf("blocked sender: %d", r.Code)
	}
	if r := send(w.b, conv, "I blocked you"); r.Code != 403 {
		t.Errorf("the blocker cannot send either: %d", r.Code)
	}
	if r := w.a.Post("/api/v1/conversations", map[string]any{"user_id": w.bID}); r.Code != 403 {
		t.Errorf("start with blocker: %d", r.Code)
	}
	// history stays readable, public content is not affected
	if len(w.a.Get("/api/v1/conversations/"+conv+"/messages").List()) != 1 {
		t.Error("history remains")
	}
	if w.env.Client().Get("/api/v1/users/"+w.b.Get("/api/v1/me").Data()["slug"].(string)).Code != 200 {
		t.Error("blocking must not hide public profile")
	}
	w.b.Delete("/api/v1/users/" + w.aID + "/block")
	if send(w.a, conv, "unblocked").Code != 201 {
		t.Error("after unblock")
	}
	if w.a.Put("/api/v1/users/"+w.aID+"/block", nil).Code != 422 {
		t.Error("cannot block oneself")
	}
}

func TestReadCursorAndMute(t *testing.T) {
	w := newWorld(t)
	conv := w.start(t, w.a, w.bID)
	w.b.Post("/api/v1/conversations/"+conv+"/accept", nil)
	for i := 0; i < 5; i++ {
		send(w.a, conv, fmt.Sprintf("m%d", i))
	}
	unread := func(c *servertest.Client) float64 {
		return c.Get("/api/v1/conversations").List()[0].(map[string]any)["unread_count"].(float64)
	}
	if unread(w.b) != 5 || unread(w.a) != 0 {
		t.Fatalf("unread: b=%v a=%v", unread(w.b), unread(w.a))
	}
	w.b.Post("/api/v1/conversations/"+conv+"/read", map[string]any{"seq": 3})
	if unread(w.b) != 2 {
		t.Errorf("after read 3: %v", unread(w.b))
	}
	w.b.Post("/api/v1/conversations/"+conv+"/read", map[string]any{"seq": 1}) // never decreases
	if unread(w.b) != 2 {
		t.Errorf("cursor must not move back: %v", unread(w.b))
	}
	w.b.Post("/api/v1/conversations/"+conv+"/read", map[string]any{"seq": 999}) // clamped to last_seq
	if unread(w.b) != 0 {
		t.Errorf("clamped: %v", unread(w.b))
	}
	// mute removes the dialog from the total
	send(w.a, conv, "one more")
	if w.b.Get("/api/v1/conversations/unread-count").Data()["total"].(float64) != 1 {
		t.Error("unread total")
	}
	if w.b.Put("/api/v1/conversations/"+conv+"/mute", nil).Code != 204 {
		t.Fatal("mute")
	}
	if w.b.Get("/api/v1/conversations/unread-count").Data()["total"].(float64) != 0 || w.b.Get("/api/v1/conversations").List()[0].(map[string]any)["muted"] != true {
		t.Error("muted dialog is excluded from the badge")
	}
	w.b.Delete("/api/v1/conversations/" + conv + "/mute")
}

func TestAntiSpamLimits(t *testing.T) {
	w := newWorld(t)
	// the same text to many conversations triggers the mass-mailing guard
	var others []string
	for i := 0; i < 6; i++ {
		c := w.env.LoginAs(fmt.Sprintf("victim%d@example.com", i))
		others = append(others, c.UserID())
	}
	w.env.Server.Pool.Exec(context.Background(), `UPDATE users SET created_at = now() - interval '3 days'`)
	codes := []int{}
	for _, o := range others {
		conv := w.start(t, w.a, o)
		codes = append(codes, send(w.a, conv, "Buy my stuff now!!!").Code)
	}
	if codes[0] != 201 || codes[3] != 201 || codes[4] != 429 {
		t.Fatalf("mass messaging guard: %v", codes)
	}
	// a different text is fine
	if send(w.a, w.start(t, w.a, others[5]), "a personal note").Code != 201 {
		t.Error("different text must pass")
	}
	// body rules
	conv := w.start(t, w.b, w.cID)
	if send(w.b, conv, "   ").Code != 422 || send(w.b, conv, strings.Repeat("x", 4001)).Code != 422 {
		t.Error("body validation")
	}
	if r := w.b.Post("/api/v1/conversations/"+conv+"/messages", map[string]any{"body": "no client id"}); r.Code != 422 {
		t.Errorf("client_id is required: %d", r.Code)
	}
}

func TestNewAccountRestrictions(t *testing.T) {
	env := servertest.New(t)
	a := env.LoginAs("young@example.com")
	b := env.LoginAs("peer@example.com")
	bID := b.UserID()
	conv := a.Post("/api/v1/conversations", map[string]any{"user_id": bID}).Data()["id"].(string)
	if r := send(a, conv, "look at https://spam.example now"); r.Code != 422 {
		t.Errorf("link from a new account to a stranger: %d %s", r.Code, r.Raw)
	}
	if send(a, conv, "hello, no links").Code != 201 {
		t.Error("plain text is fine")
	}
	// at most 5 new dialogs per day for accounts younger than 24h
	codes := []int{}
	for i := 0; i < 6; i++ {
		c := env.LoginAs(fmt.Sprintf("target%d@example.com", i))
		codes = append(codes, a.Post("/api/v1/conversations", map[string]any{"user_id": c.UserID()}).Code)
	}
	if codes[3] != 201 || codes[4] != 429 {
		t.Fatalf("new-dialog limit for young accounts (1 used + 4 more allowed): %v", codes)
	}
}

func readEvent(t *testing.T, sc *bufio.Scanner, timeout time.Duration) (event, data string) {
	t.Helper()
	type res struct{ ev, data string }
	ch := make(chan res, 1)
	go func() {
		var ev, d string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				d = strings.TrimPrefix(line, "data: ")
			case line == "" && ev != "":
				ch <- res{ev, d}
				return
			}
		}
		ch <- res{}
	}()
	select {
	case r := <-ch:
		return r.ev, r.data
	case <-time.After(timeout):
		return "", ""
	}
}

func openStream(t *testing.T, base string, c *servertest.Client) (*http.Response, *bufio.Scanner) {
	req, _ := http.NewRequest("GET", base+"/api/v1/events", nil)
	req.AddCookie(&http.Cookie{Name: "bl_session", Value: c.Cookie()})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp, bufio.NewScanner(resp.Body)
}

func TestRealtimeDeliveryThroughOutbox(t *testing.T) {
	w := newWorld(t)
	w.env.StartBackground()
	srv := w.env.HTTPServer()
	respB, scB := openStream(t, srv.URL, w.b)
	defer respB.Body.Close()
	respC, scC := openStream(t, srv.URL, w.c)
	defer respC.Body.Close()
	if respB.Header.Get("Content-Type") != "text/event-stream" || respB.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers: %v", respB.Header)
	}
	if ev, _ := readEvent(t, scB, 3*time.Second); ev != "ready" {
		t.Fatalf("first event: %q", ev)
	}
	readEvent(t, scC, 3*time.Second)

	conv := w.start(t, w.a, w.bID)
	secret := "very secret words"
	send(w.a, conv, secret)
	ev, data := readEvent(t, scB, 5*time.Second)
	if ev != "message" || !strings.Contains(data, secret) || !strings.Contains(data, conv) {
		t.Fatalf("B must receive the message: %q %q", ev, data)
	}
	// the Stream carries identifiers only
	msgs, _ := w.env.Server.Redis.XRange(context.Background(), "events:main", "-", "+").Result()
	for _, m := range msgs {
		if strings.Contains(fmt.Sprint(m.Values), secret) {
			t.Fatal("message text must never be written to the event stream")
		}
	}
	// a third user receives nothing
	if ev, data := readEvent(t, scC, 700*time.Millisecond); ev != "" {
		t.Fatalf("stranger got an event: %q %q", ev, data)
	}
	// read receipts reach the sender
	respA, scA := openStream(t, srv.URL, w.a)
	defer respA.Body.Close()
	readEvent(t, scA, 3*time.Second)
	w.b.Post("/api/v1/conversations/"+conv+"/accept", nil)
	if ev, _ := readEvent(t, scA, 5*time.Second); ev != "conversation" {
		t.Errorf("status change event: %q", ev)
	}
	w.b.Post("/api/v1/conversations/"+conv+"/read", map[string]any{"seq": 1})
	if ev, data := readEvent(t, scA, 5*time.Second); ev != "read" || !strings.Contains(data, `"read_seq":1`) {
		t.Errorf("read event: %q %q", ev, data)
	}
}

func TestStreamLimitAndAuth(t *testing.T) {
	w := newWorld(t)
	srv := w.env.HTTPServer()
	anon, _ := http.Get(srv.URL + "/api/v1/events")
	if anon.StatusCode != 401 {
		t.Errorf("anonymous stream: %d", anon.StatusCode)
	}
	var open []*http.Response
	defer func() {
		for _, r := range open {
			r.Body.Close()
		}
	}()
	for i := 0; i < 5; i++ {
		r, _ := openStream(t, srv.URL, w.a)
		if r.StatusCode != 200 {
			t.Fatalf("stream %d: %d", i, r.StatusCode)
		}
		open = append(open, r)
	}
	r6, _ := openStream(t, srv.URL, w.a)
	defer r6.Body.Close()
	if r6.StatusCode != 429 {
		t.Errorf("6th stream: %d", r6.StatusCode)
	}
}
