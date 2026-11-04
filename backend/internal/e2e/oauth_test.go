package e2e_test

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"beeloy.com/backend/internal/articles"
	"beeloy.com/backend/internal/identity"
	"beeloy.com/backend/internal/server/servertest"
	"github.com/google/uuid"
)

// stubProvider replaces Google/Facebook: code "ok:<subject>[:<email>]" proves that subject.
type stubProvider struct{ name identity.Provider }

func (s stubProvider) AuthorizationURL(state, _, _ string) string {
	return "https://stub.example/auth?state=" + url.QueryEscape(state)
}

func (s stubProvider) ExchangeAndVerify(_ context.Context, code, _, _ string) (identity.ProviderIdentity, error) {
	parts := strings.Split(code, ":")
	if len(parts) < 2 || parts[0] != "ok" {
		return identity.ProviderIdentity{}, context.DeadlineExceeded
	}
	pi := identity.ProviderIdentity{Provider: s.name, Subject: parts[1]}
	if len(parts) > 2 {
		pi.Email = parts[2]
	}
	return pi, nil
}

var regexp6 = regexp.MustCompile(`\b\d{6}\b`)

func withStubs(env *servertest.Env) {
	env.Server.Identity.OAuth.Providers[identity.ProviderGoogle] = stubProvider{identity.ProviderGoogle}
	env.Server.Identity.OAuth.Providers[identity.ProviderFacebook] = stubProvider{identity.ProviderFacebook}
}

func stateOf(t *testing.T, loc string) string {
	u, err := url.Parse(loc)
	if err != nil || u.Query().Get("state") == "" {
		t.Fatalf("no state in %q", loc)
	}
	return u.Query().Get("state")
}

func start(t *testing.T, c *servertest.Client, provider string) string {
	r := c.Get("/api/v1/auth/oauth/" + provider + "/start?return_to=/en/ideas&locale=en")
	if r.Code != 302 || c.Cookies[identity.OAuthFlowCookie] == "" {
		t.Fatalf("start: %d %s", r.Code, r.Raw)
	}
	return stateOf(t, r.Header.Get("Location"))
}

func TestSocialOnboardingLoginAndReplay(t *testing.T) {
	env := servertest.New(t)
	withStubs(env)
	c := env.Client()
	state := start(t, c, "google")
	r := c.Get("/api/v1/auth/oauth/google/callback?code=ok:g-1&state=" + url.QueryEscape(state))
	if r.Code != 303 || !strings.Contains(r.Header.Get("Location"), "step=onboarding") || c.Cookies[identity.OnboardingCookie] == "" {
		t.Fatalf("callback: %d %s", r.Code, r.Header)
	}
	if c.Cookies["bl_session"] != "" {
		t.Fatal("no session before onboarding")
	}
	// a replayed state never works again
	other := env.Client()
	other.Cookies[identity.OAuthFlowCookie] = "x"
	if r := other.Get("/api/v1/auth/oauth/google/callback?code=ok:g-1&state=" + url.QueryEscape(state)); r.Code != 303 || !strings.Contains(r.Header.Get("Location"), "error=oauth") {
		t.Fatalf("replayed state: %d %s", r.Code, r.Header)
	}
	if r := c.Post("/api/v1/auth/oauth/complete", map[string]any{"locale": "en", "accept_terms": false}); r.Code != 422 {
		t.Fatalf("terms required: %d %s", r.Code, r.Raw)
	}
	r = c.Post("/api/v1/auth/oauth/complete", map[string]any{"locale": "en", "accept_terms": true})
	if r.Code != 200 || r.Data()["is_new"] != true {
		t.Fatalf("complete: %d %s", r.Code, r.Raw)
	}
	u := r.Data()["user"].(map[string]any)
	if u["nickname"] != "User" || !strings.HasPrefix(u["slug"].(string), "user-") {
		t.Fatalf("neutral public name expected: %v", u)
	}
	// the onboarding token is single-use
	if r := c.Post("/api/v1/auth/oauth/complete", map[string]any{"locale": "en", "accept_terms": true}); r.Code != 401 {
		t.Fatalf("token reuse: %d %s", r.Code, r.Raw)
	}
	// the same provider subject now signs in directly and lands on return_to
	c2 := env.Client()
	s2 := start(t, c2, "google")
	r = c2.Get("/api/v1/auth/oauth/google/callback?code=ok:g-1&state=" + url.QueryEscape(s2))
	if r.Code != 303 || r.Header.Get("Location") != "/en/ideas" || c2.Cookies["bl_session"] == "" {
		t.Fatalf("login: %d %s", r.Code, r.Header)
	}
	if me := c2.Get("/api/v1/me"); me.Code != 200 || me.Data()["id"] != u["id"] {
		t.Fatalf("me: %d %s", me.Code, me.Raw)
	}
}

func TestSocialStateMustMatchBrowser(t *testing.T) {
	env := servertest.New(t)
	withStubs(env)
	victim, attacker := env.Client(), env.Client()
	state := start(t, victim, "google")
	attacker.Cookies[identity.OAuthFlowCookie] = "forged"
	r := attacker.Get("/api/v1/auth/oauth/google/callback?code=ok:g-2&state=" + url.QueryEscape(state))
	if !strings.Contains(r.Header.Get("Location"), "error=oauth") || attacker.Cookies[identity.OnboardingCookie] != "" {
		t.Fatalf("foreign flow cookie accepted: %d %s", r.Code, r.Header)
	}
	if r := attacker.Get("/api/v1/auth/oauth/github/start"); r.Code != 404 {
		t.Fatalf("unknown provider: %d", r.Code)
	}
	state = start(t, victim, "google")
	r = victim.Get("/api/v1/auth/oauth/google/callback?code=bad&state=" + url.QueryEscape(state))
	if !strings.Contains(r.Header.Get("Location"), "error=oauth") || strings.Contains(r.Header.Get("Location"), "bad") {
		t.Fatalf("provider failure must redirect without details: %v", r.Header)
	}
}

func TestSocialDoesNotMergeByEmailAndLinking(t *testing.T) {
	env := servertest.New(t)
	withStubs(env)
	alice := env.LoginAs("alice@example.com")
	aliceID := alice.UserID()

	// a Google identity that reports alice's e-mail still creates a separate account
	g := env.Client()
	st := start(t, g, "google")
	g.Get("/api/v1/auth/oauth/google/callback?code=ok:g-alice:alice@example.com&state=" + url.QueryEscape(st))
	r := g.Post("/api/v1/auth/oauth/complete", map[string]any{"locale": "en", "accept_terms": true})
	if r.Code != 200 || r.Data()["user"].(map[string]any)["id"] == aliceID {
		t.Fatalf("e-mail must not merge accounts: %d %s", r.Code, r.Raw)
	}

	// alice links Facebook from her own session
	lr := alice.Post("/api/v1/me/identities/facebook/link", map[string]any{"return_to": "/en/settings"})
	if lr.Code != 200 {
		t.Fatalf("link start: %d %s", lr.Code, lr.Raw)
	}
	fst := stateOf(t, lr.Data()["authorization_url"].(string))
	// the callback in a browser without her session is refused
	stranger := env.Client()
	stranger.Cookies[identity.OAuthFlowCookie] = alice.Cookies[identity.OAuthFlowCookie]
	if r := stranger.Get("/api/v1/auth/oauth/facebook/callback?code=ok:f-1&state=" + url.QueryEscape(fst)); !strings.Contains(r.Header.Get("Location"), "error=oauth") {
		t.Fatalf("link outside the session: %d %s", r.Code, r.Header)
	}
	lr = alice.Post("/api/v1/me/identities/facebook/link", nil)
	fst = stateOf(t, lr.Data()["authorization_url"].(string))
	if r := alice.Get("/api/v1/auth/oauth/facebook/callback?code=ok:f-1&state=" + url.QueryEscape(fst)); r.Code != 303 {
		t.Fatalf("link callback: %d %s", r.Code, r.Header)
	}
	list := alice.Get("/api/v1/me/identities").List()
	if len(list) != 2 || strings.Contains(string(alice.Get("/api/v1/me/identities").Raw), "f-1") {
		t.Fatalf("identities: %v", list)
	}
	// an identity owned by someone else is never re-linked
	bob := env.LoginAs("bob@example.com")
	bl := bob.Post("/api/v1/me/identities/facebook/link", nil)
	bst := stateOf(t, bl.Data()["authorization_url"].(string))
	if r := bob.Get("/api/v1/auth/oauth/facebook/callback?code=ok:f-1&state=" + url.QueryEscape(bst)); !strings.Contains(r.Header.Get("Location"), "error=oauth") {
		t.Fatalf("foreign identity linked: %d %s", r.Code, r.Header)
	}
	// unlink both ways: the last method cannot be removed
	var fbID, otpID string
	for _, it := range list {
		m := it.(map[string]any)
		if m["provider"] == "facebook" {
			fbID = m["id"].(string)
		} else {
			otpID = m["id"].(string)
		}
	}
	if r := alice.Delete("/api/v1/me/identities/" + fbID); r.Code != 204 {
		t.Fatalf("unlink: %d %s", r.Code, r.Raw)
	}
	if r := alice.Delete("/api/v1/me/identities/" + otpID); r.Code != 409 || !strings.Contains(string(r.Raw), "last_identity") {
		t.Fatalf("last identity: %d %s", r.Code, r.Raw)
	}
	if r := bob.Delete("/api/v1/me/identities/" + otpID); r.Code != 404 {
		t.Fatalf("someone else's identity: %d", r.Code)
	}
}

func TestOTPConcurrentVerifySingleSuccess(t *testing.T) {
	env := servertest.New(t)
	c := env.Client()
	if r := c.Post("/api/v1/auth/otp/request", map[string]any{"email": "race@example.com", "locale": "en"}); r.Code != 202 {
		t.Fatalf("request: %d", r.Code)
	}
	msgs := env.Mailer.Messages()
	code := regexp6.FindString(msgs[len(msgs)-1].TextBody)
	var wg sync.WaitGroup
	ok := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cl := env.Client().WithIP("203.0.113." + string(rune('1'+i)))
			r := cl.Post("/api/v1/auth/otp/verify", map[string]any{"email": "race@example.com", "code": code, "accept_terms": true, "locale": "en"})
			if r.Code == 200 {
				ok <- 1
			}
		}(i)
	}
	wg.Wait()
	close(ok)
	if n := len(ok); n != 1 {
		t.Fatalf("exactly one verification may succeed, got %d", n)
	}
}

func TestOTPFifthWrongAttemptBurnsCode(t *testing.T) {
	env := servertest.New(t)
	c := env.Client()
	c.Post("/api/v1/auth/otp/request", map[string]any{"email": "burn@example.com", "locale": "en"})
	msgs := env.Mailer.Messages()
	code := regexp6.FindString(msgs[len(msgs)-1].TextBody)
	for i := 0; i < 5; i++ {
		cl := env.Client().WithIP("203.0.113." + string(rune('1'+i)))
		if r := cl.Post("/api/v1/auth/otp/verify", map[string]any{"email": "burn@example.com", "code": "000000", "accept_terms": true}); r.Code == 200 {
			t.Fatal("wrong code accepted")
		}
	}
	if r := env.Client().WithIP("203.0.113.9").Post("/api/v1/auth/otp/verify", map[string]any{"email": "burn@example.com", "code": code, "accept_terms": true}); r.Code == 200 {
		t.Fatal("the correct code must be burned after the fifth wrong attempt")
	}
}

func TestLastAdminSurvivesConcurrentDeletion(t *testing.T) {
	env := servertest.New(t)
	a, b := env.LoginAs("adm1@example.com"), env.LoginAs("adm2@example.com")
	env.Grant(a.UserID(), "admin", nil)
	env.Grant(b.UserID(), "admin", nil)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, c := range []*servertest.Client{a, b} {
		wg.Add(1)
		go func(c *servertest.Client) { defer wg.Done(); codes <- c.Delete("/api/v1/me").Code }(c)
	}
	wg.Wait()
	close(codes)
	accepted, conflict := 0, 0
	for code := range codes {
		switch code {
		case 202:
			accepted++
		case 409:
			conflict++
		}
	}
	if accepted != 1 || conflict != 1 {
		t.Fatalf("exactly one admin may leave: accepted=%d conflict=%d", accepted, conflict)
	}
}

// A closed article takes its comments, replies, permalink and report target with it, on the very next request.
func TestUnpublishClosesCommentsAtOnce(t *testing.T) {
	w := newWorld(t)
	id := w.editor.Post("/api/v1/editor/articles", map[string]any{"category_id": servertest.CatNews, "source_locale": "en", "title_for_slug": "Closing story"}).Data()["id"].(string)
	w.editor.Put("/api/v1/editor/articles/"+id+"/source", map[string]any{"title": "Closing story title", "summary": "A summary for closing.", "body_md": "body of the story"})
	w.editor.Post("/api/v1/editor/articles/"+id+"/submit", nil)
	if r := w.mod.Post("/api/v1/mod/articles/"+id+"/publish", map[string]any{"rev": 1, "visibility": "public"}); r.Code != 200 {
		t.Fatalf("publish: %d %s", r.Code, r.Raw)
	}
	cm := w.alice.Post("/api/v1/comments", map[string]any{"target_type": "article", "target_id": id, "body": "first!"})
	if cm.Code != 201 {
		t.Fatalf("comment: %d %s", cm.Code, cm.Raw)
	}
	cid := cm.Data()["id"].(string)
	rp := w.bob.Post("/api/v1/comments", map[string]any{"target_type": "article", "target_id": id, "parent_id": cid, "body": "reply"})
	if rp.Code != 201 {
		t.Fatalf("reply: %d %s", rp.Code, rp.Raw)
	}
	list := "/api/v1/comments?target_type=article&target_id=" + id
	if w.anon.Get(list).Code != 200 || w.anon.Get("/api/v1/comments/"+cid+"/replies").Code != 200 || w.anon.Get("/api/v1/resources/"+cid).Code != 200 {
		t.Fatal("open article: comments are public")
	}
	if r := w.mod.Post("/api/v1/mod/articles/"+id+"/unpublish", nil); r.Code != 200 {
		t.Fatalf("unpublish: %d %s", r.Code, r.Raw)
	}
	for name, path := range map[string]string{"list": list, "replies": "/api/v1/comments/" + cid + "/replies", "permalink": "/api/v1/resources/" + cid, "article": "/api/v1/resources/" + id} {
		if code := w.anon.Get(path).Code; code != 404 {
			t.Errorf("%s of a closed article: %d", name, code)
		}
	}
	if r := w.eve.Post("/api/v1/reports", map[string]any{"target_type": "comment", "target_id": cid, "reason": "spam"}); r.Code != 404 {
		t.Errorf("report on a closed target: %d %s", r.Code, r.Raw)
	}
}

// Revision files are immutable: a second write to the same key never succeeds, and a newer revision stays invisible
// until it is approved again.
func TestRevisionFilesAreImmutable(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id := w.editor.Post("/api/v1/editor/articles", map[string]any{"category_id": servertest.CatNews, "source_locale": "en", "title_for_slug": "Immutable story"}).Data()["id"].(string)
	src := map[string]any{"title": "Immutable story title", "summary": "A summary of the immutable story.", "body_md": "first revision text"}
	w.editor.Put("/api/v1/editor/articles/"+id+"/source", src)
	tr := map[string]any{"title": "Titre immuable", "summary": "Un resume de l'histoire.", "body_md": "texte de la premiere revision"}
	if r := w.editor.Put("/api/v1/editor/articles/"+id+"/translations/fr", tr); r.Code != 200 && r.Code != 204 {
		t.Fatalf("first translation: %d %s", r.Code, r.Raw)
	}
	tr["body_md"] = "texte modifie en cachette"
	if r := w.editor.Put("/api/v1/editor/articles/"+id+"/translations/fr", tr); r.Code != 409 {
		t.Fatalf("second write to the same revision file: %d %s", r.Code, r.Raw)
	}
	uid := uuid.MustParse(id)
	b, err := w.env.Store.Get(ctx, articles.LocaleKey(uid, 1, "fr"), 1<<20)
	if err != nil || !strings.Contains(string(b), "premiere revision") || strings.Contains(string(b), "cachette") {
		t.Fatalf("stored file changed: %s %v", b, err)
	}
	// a machine file is protected as well
	mach := articles.LocaleFile{SchemaVersion: 1, ArticleID: uid, Locale: "de", Rev: 1, Origin: "machine", Title: "Titel", Summary: "Zusammenfassung hier", BodyMD: "Text", GeneratedAt: time.Now()}
	mb, _ := json.Marshal(mach)
	w.env.Store.PutIfAbsent(ctx, articles.LocaleKey(uid, 1, "de"), mb, "application/json")
	newSyncer(w.env).RunOnce(ctx)
	if r := w.editor.Put("/api/v1/editor/articles/"+id+"/translations/de", tr); r.Code != 409 {
		t.Fatalf("a registered machine file must not be replaced: %d %s", r.Code, r.Raw)
	}
	// publish rev 1; rev 2 stays invisible and the manifest rev never decreases
	w.editor.Post("/api/v1/editor/articles/"+id+"/submit", nil)
	w.mod.Post("/api/v1/mod/articles/"+id+"/publish", map[string]any{"rev": 1, "visibility": "public"})
	src["body_md"] = "second revision text"
	if r := w.editor.Put("/api/v1/editor/articles/"+id+"/source", src); r.Code != 200 {
		t.Fatalf("second revision: %d %s", r.Code, r.Raw)
	}
	if r := w.anon.Get("/api/v1/articles/immutable-story?locale=en"); r.Code != 200 || !strings.Contains(string(r.Raw), "first revision text") || strings.Contains(string(r.Raw), "second revision") {
		t.Fatalf("readers keep seeing the approved revision: %d %s", r.Code, r.Raw)
	}
	mraw, _ := w.env.Store.Get(ctx, articles.ManifestKey(uid), 1<<16)
	var man articles.Manifest
	json.Unmarshal(mraw, &man)
	if man.Rev != 2 {
		t.Fatalf("manifest rev: %d", man.Rev)
	}
}

// A message request admits at most three messages from the initiator even under concurrency, and a retry whose
// answer was lost returns the stored message.
func TestRequestCapHoldsUnderConcurrency(t *testing.T) {
	w := newWorld(t)
	conv := w.alice.Post("/api/v1/conversations", map[string]any{"user_id": w.bob.UserID()}).Data()["id"].(string)
	var wg sync.WaitGroup
	codes := make(chan int, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- send(w.alice, conv, "hello number "+string(rune('a'+i))+" for bob").Code
		}(i)
	}
	wg.Wait()
	close(codes)
	created := 0
	for c := range codes {
		if c == 201 {
			created++
		}
	}
	if created != 3 {
		t.Fatalf("a request admits exactly 3 messages, got %d", created)
	}
	hist := w.alice.Get("/api/v1/conversations/" + conv + "/messages").List()
	if len(hist) != 3 {
		t.Fatalf("stored messages: %d", len(hist))
	}
	last := hist[len(hist)-1].(map[string]any)
	retry := w.alice.Post("/api/v1/conversations/"+conv+"/messages", map[string]any{"client_id": last["client_id"], "body": "different text, ignored"})
	if retry.Code != 200 || retry.Data()["id"] != last["id"] || retry.Data()["seq"] != last["seq"] {
		t.Fatalf("idempotent retry: %d %s", retry.Code, retry.Raw)
	}
	// the fourth new message is refused, and the retry still answers
	if r := send(w.alice, conv, "one more please"); r.Code != 409 {
		t.Fatalf("fourth message: %d %s", r.Code, r.Raw)
	}
}
