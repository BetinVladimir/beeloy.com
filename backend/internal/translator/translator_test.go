package translator_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"beeloy.com/backend/internal/articles"
	"beeloy.com/backend/internal/contentsync"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/s3store"
	"beeloy.com/backend/internal/translator"
	"github.com/google/uuid"
)

// fakeLLM "translates" by prefixing every value with the target language code; behaviour can be bent per test.
type fakeLLM struct {
	calls   atomic.Int32
	mangle  func(call int32, dst string, in map[string]string) map[string]string
	badJSON func(call int32) bool
}

var dstRe = regexp.MustCompile(`to ([A-Za-z ]+)\.`)
var headRe = regexp.MustCompile(`^(#+ )?`)

func codeOf(name string) string {
	for _, l := range locales.All() {
		if l.EnglishName == name {
			return l.Code
		}
	}
	return "??"
}

func (f *fakeLLM) Chat(_ context.Context, system, user string, _ bool) (string, error) {
	n := f.calls.Add(1)
	if f.badJSON != nil && f.badJSON(n) {
		return "this is not json", nil
	}
	dst := codeOf(dstRe.FindStringSubmatch(system)[1])
	var in map[string]string
	_ = json.Unmarshal([]byte(user), &in)
	out := map[string]string{}
	for k, v := range in {
		out[k] = headRe.ReplaceAllString(v, "${1}["+dst+"] ")
	}
	if f.mangle != nil {
		out = f.mangle(n, dst, out)
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func seedArticle(t *testing.T, st *s3store.MemStore, body string) uuid.UUID {
	id := uuid.New()
	m := articles.Manifest{SchemaVersion: 1, ArticleID: id, Slug: "translate-me", CategoryKey: "news", SourceLocale: "en", Rev: 2, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	b, _ := json.Marshal(m)
	st.Put(context.Background(), articles.ManifestKey(id), b, "application/json")
	f := articles.LocaleFile{SchemaVersion: 1, ArticleID: id, Locale: "en", Rev: 2, Origin: "human", Title: "A good article title", Summary: "A summary that is long enough to be valid.", BodyMD: body, GeneratedAt: time.Now()}
	b, _ = json.Marshal(f)
	st.Put(context.Background(), articles.LocaleKey(id, 2, "en"), b, "application/json")
	return id
}

const body = "# Heading\n\nSome text with a link https://example.com/page and a {placeholder}.\n\n```go\nfmt.Println(\"keep me\")\n```\n\nThe last paragraph of the article."

func TestSplitMarkdown(t *testing.T) {
	text := "para one\n\n```\ncode\n\nstill code\n```\n\npara two is here\n\n" + strings.Repeat("long line of text\n", 20)
	chunks, err := translator.SplitMarkdown(text, 80)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 3 {
		t.Fatalf("must split: %d", len(chunks))
	}
	for _, c := range chunks {
		if strings.Count(c, "```")%2 != 0 {
			t.Errorf("code block was cut: %q", c)
		}
		if len([]rune(c)) > 80+40 {
			t.Errorf("chunk too large: %d", len([]rune(c)))
		}
	}
	if n := strings.Count(strings.Join(chunks, "\n\n"), "still code"); n != 1 {
		t.Error("content must survive")
	}
	if got, _ := translator.SplitMarkdown("one short paragraph", 3000); len(got) != 1 || got[0] != "one short paragraph" {
		t.Errorf("short text: %v", got)
	}
	if got, _ := translator.SplitMarkdown("", 100); len(got) != 0 {
		t.Error("empty text")
	}
}

func TestSplitMarkdownNeverCutsFencesOrTables(t *testing.T) {
	long := strings.Repeat("let x = 1;\n", 30)
	for _, fence := range []string{"```", "~~~", "````"} {
		if _, err := translator.SplitMarkdown("intro\n\n"+fence+"\n"+long+fence+"\n\noutro", 100); !errors.Is(err, translator.ErrChunkTooLarge) {
			t.Errorf("%s block over the limit must be rejected, got %v", fence, err)
		}
	}
	// a shorter fence inside a longer one does not close it; blank lines inside stay inside
	ok, err := translator.SplitMarkdown("````\n```\n\ninner\n```\n````\n\nafter", 200)
	if err != nil || len(ok) != 1 || !strings.Contains(ok[0], "inner") {
		t.Errorf("nested fence: %v %v", ok, err)
	}
	table := "| a | b |\n|---|---|\n" + strings.Repeat("| 1 | 2 |\n", 40)
	if _, err := translator.SplitMarkdown(table, 100); !errors.Is(err, translator.ErrChunkTooLarge) {
		t.Errorf("table: %v", err)
	}
	// an ordinary long line is cut at words and loses nothing
	line := strings.TrimSpace(strings.Repeat("word ", 100))
	got, err := translator.SplitMarkdown(line, 50)
	if err != nil || strings.Join(strings.Fields(strings.Join(got, " ")), " ") != line {
		t.Errorf("long line: %v %v", err, got)
	}
}

func TestValidateChunk(t *testing.T) {
	src := "Read https://a.example/x and {name}.\n\n```\ncode\n```\n\n# Title"
	ok := "Lies https://a.example/x und {name}.\n\n```\ncode\n```\n\n# Titel"
	if err := translator.ValidateChunk(src, ok, "de"); err != nil {
		t.Fatalf("good translation rejected: %v", err)
	}
	bad := map[string]string{
		"lost url":      strings.Replace(ok, "https://a.example/x", "", 1),
		"changed code":  strings.Replace(ok, "code", "kode", 1),
		"lost fence":    strings.Replace(ok, "```\ncode\n```", "code", 1),
		"placeholder":   strings.Replace(ok, "{name}", "{nam}", 1),
		"lost heading":  strings.Replace(ok, "# Titel", "Titel", 1),
		"empty":         "  ",
		"way too long":  ok + strings.Repeat(" blah", 200),
		"way too short": "x",
	}
	for name, dst := range bad {
		if translator.ValidateChunk(src, dst, "de") == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	long := strings.Repeat("An untranslated sentence. ", 5)
	if translator.ValidateChunk(long, long, "de") == nil {
		t.Error("an untranslated long text must be rejected")
	}
	if translator.ValidateChunk("short", "short", "de") != nil {
		t.Error("short strings may stay equal")
	}
	if translator.ValidateChunk(strings.Repeat("a", 100), strings.Repeat("字", 20), "zh") != nil {
		t.Error("CJK ratio is lenient")
	}
}

func TestOllamaClient(t *testing.T) {
	var calls atomic.Int32
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"qwen:latest"},{"name":"llama3:8b"}]}`))
		case "/api/chat":
			n := calls.Add(1)
			if down.Load() {
				http.Error(w, "down", 503)
				return
			}
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			if req["format"] != "json" || req["stream"] != false || req["model"] != "llama3:8b" {
				t.Errorf("request: %v", req)
			}
			switch n {
			case 1, 2:
				http.Error(w, "busy", 503)
			default:
				w.Write([]byte(`{"message":{"role":"assistant","content":"{\"ok\":\"yes\"}"}}`))
			}
		}
	}))
	defer srv.Close()
	c := translator.NewClient(srv.URL, "llama3:8b", 5*time.Second)
	c.Delays = []time.Duration{time.Millisecond, time.Millisecond}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := translator.NewClient(srv.URL, "missing", time.Second).Ping(context.Background()); err == nil {
		t.Error("missing model must fail")
	}
	out, err := c.Chat(context.Background(), "sys", "{}", true)
	if err != nil || !strings.Contains(out, "yes") || calls.Load() != 3 {
		t.Fatalf("retry on 503: %q %v calls=%d", out, err, calls.Load())
	}
	down.Store(true) // all following answers are 503
	c.Delays = []time.Duration{time.Millisecond}
	if _, err := c.Chat(context.Background(), "s", "{}", true); err == nil {
		t.Error("persistent 503 must fail after retries")
	}
	if err := translator.NewClient("http://127.0.0.1:1", "m", time.Second).Ping(context.Background()); err == nil {
		t.Error("unreachable Ollama")
	}
}

func TestRunArticleWritesMachineFilesOnly(t *testing.T) {
	st := s3store.NewMem()
	id := seedArticle(t, st, body)
	manifestBefore, _ := st.Get(context.Background(), articles.ManifestKey(id), 1<<20)
	// a human translation exists for German and an old machine one for French
	human := articles.LocaleFile{SchemaVersion: 1, ArticleID: id, Locale: "de", Rev: 2, Origin: "human", Title: "Von Hand", Summary: "Eine handgeschriebene Zusammenfassung.", BodyMD: "Handarbeit", GeneratedAt: time.Now()}
	hb, _ := json.Marshal(human)
	st.Put(context.Background(), articles.LocaleKey(id, 2, "de"), hb, "application/json")

	llm := &fakeLLM{}
	tr := &translator.Translator{LLM: llm, Model: "test-model", Keep: []string{"Beeloy"}, MaxChunk: 80}
	res, err := tr.RunArticle(context.Background(), st, id.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]translator.LocaleResult{}
	for _, r := range res {
		by[r.Locale] = r
	}
	if len(res) != 25 || by["de"].Status != translator.Skipped || by["fr"].Status != translator.Translated || by["bg"].Status != translator.Translated {
		t.Fatalf("results: %d de=%v fr=%v", len(res), by["de"], by["fr"])
	}
	b, err := st.Get(context.Background(), articles.LocaleKey(id, 2, "fr"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var f articles.LocaleFile
	json.Unmarshal(b, &f)
	if f.Origin != "machine" || f.Model == nil || *f.Model != "test-model" || f.Rev != 2 || !strings.Contains(f.Title, "[fr]") || !strings.Contains(f.BodyMD, "```go") {
		t.Fatalf("file: %+v", f)
	}
	// human file untouched
	tr.RunArticle(context.Background(), st, id.String(), []string{"de"})
	if b, _ := st.Get(context.Background(), articles.LocaleKey(id, 2, "de"), 1<<20); string(b) != string(hb) {
		t.Fatal("a human translation must never be overwritten")
	}
	// manifest and the source file are untouched
	if after, _ := st.Get(context.Background(), articles.ManifestKey(id), 1<<20); string(after) != string(manifestBefore) {
		t.Fatal("manifest.json must not be modified")
	}
	// second run: everything exists -> skipped, the model is not called
	before := llm.calls.Load()
	res, _ = tr.RunArticle(context.Background(), st, id.String(), nil)
	for _, r := range res {
		if r.Status != translator.Skipped {
			t.Errorf("%s: %v", r.Locale, r.Status)
		}
	}
	if llm.calls.Load() != before {
		t.Error("no model calls when nothing is missing")
	}
	// machine files are immutable too: there is no way to replace them
	frBefore, _ := st.Get(context.Background(), articles.LocaleKey(id, 2, "fr"), 1<<20)
	if res, _ = tr.RunArticle(context.Background(), st, id.String(), []string{"fr"}); res[0].Status != translator.Skipped {
		t.Errorf("existing machine file: %v", res)
	}
	if frAfter, _ := st.Get(context.Background(), articles.LocaleKey(id, 2, "fr"), 1<<20); string(frAfter) != string(frBefore) {
		t.Fatal("a machine translation must not change")
	}
}

func TestRetriesAndFailures(t *testing.T) {
	st := s3store.NewMem()
	id := seedArticle(t, st, body)
	// garbage twice, then good: succeeds
	flaky := &fakeLLM{badJSON: func(n int32) bool { return n <= 2 }}
	tr := &translator.Translator{LLM: flaky, Model: "m", MaxChunk: 10000}
	res, _ := tr.RunArticle(context.Background(), st, id.String(), []string{"fr"})
	if res[0].Status != translator.Translated {
		t.Fatalf("third attempt must succeed: %v", res[0])
	}
	// the model keeps dropping a URL: the locale fails, nothing is written, others continue
	broken := &fakeLLM{mangle: func(n int32, dst string, out map[string]string) map[string]string {
		if dst == "it" {
			if v, ok := out["body_md"]; ok {
				out["body_md"] = strings.ReplaceAll(v, "https://example.com/page", "")
			}
		}
		return out
	}}
	tr2 := &translator.Translator{LLM: broken, Model: "m", MaxChunk: 10000}
	res, _ = tr2.RunArticle(context.Background(), st, id.String(), []string{"it", "es"})
	if res[0].Status != translator.Failed || !strings.Contains(res[0].Reason, "URLs") || res[1].Status != translator.Translated {
		t.Fatalf("results: %+v", res)
	}
	if ok, _ := st.Exists(context.Background(), articles.LocaleKey(id, 2, "it")); ok {
		t.Fatal("a failed locale must not be written")
	}
}

func uiSource(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "en.json")
	os.WriteFile(p, []byte(`{"app.name":"Beeloy","greet.user":"Hello {name}, welcome back to the community","items.one":"{count} item","items.other":"{count} items"}`), 0o644)
	return p
}

func TestRunUIBuildsValidReleaseAndPublishesManifestLast(t *testing.T) {
	st := s3store.NewMem()
	out := t.TempDir()
	tr := &translator.Translator{LLM: &fakeLLM{}, Model: "m"}
	// without --publish nothing reaches S3
	res, err := tr.RunUI(context.Background(), translator.UIOptions{SourcePath: uiSource(t), Version: "v1", OutDir: out})
	if err != nil || len(res) != 25 {
		t.Fatalf("run: %v %d", err, len(res))
	}
	if err := st.List(context.Background(), "", func(s3store.Object) error { return fmt.Errorf("S3 must be empty") }); err != nil {
		t.Fatal(err)
	}
	// the produced release passes the Content Sync validation
	var m contentsync.Manifest
	mb, err := os.ReadFile(filepath.Join(out, "v1", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(mb, &m)
	if err := contentsync.ValidateManifest(m, locales.Codes()); err != nil {
		t.Fatal(err)
	}
	bundles := map[string]contentsync.Bundle{}
	for _, it := range m.Locales {
		raw, err := os.ReadFile(filepath.Join(out, "v1", it.File))
		if err != nil {
			t.Fatal(err)
		}
		b, err := contentsync.ValidateBundle(it, raw)
		if err != nil {
			t.Fatal(err)
		}
		bundles[it.Code] = b
	}
	if problems := contentsync.ValidateSet(m, bundles); len(problems) != 0 {
		t.Fatalf("release rejected by sync: %v", problems)
	}
	if bundles["bg"].Messages["greet.user"] != "[bg] Hello {name}, welcome back to the community" {
		t.Errorf("bg: %v", bundles["bg"].Messages)
	}
	// publishing: files first, manifest last; the same version cannot be published twice
	res, err = tr.RunUI(context.Background(), translator.UIOptions{SourcePath: uiSource(t), Version: "v2", OutDir: out, Publish: true, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Exists(context.Background(), "i18n/manifest.json"); !ok {
		t.Fatal("manifest must be uploaded")
	}
	if ok, _ := st.Exists(context.Background(), "i18n/releases/v2/uk.json"); !ok {
		t.Fatal("files must be uploaded")
	}
	if _, err := tr.RunUI(context.Background(), translator.UIOptions{SourcePath: uiSource(t), Version: "v2", Publish: true, Store: st}); err == nil {
		t.Fatal("version reuse must fail")
	}
}

func TestRunUIFailureWritesNoManifest(t *testing.T) {
	st := s3store.NewMem()
	out := t.TempDir()
	llm := &fakeLLM{mangle: func(n int32, dst string, o map[string]string) map[string]string {
		if dst == "pl" {
			o["greet.user"] = "Witaj {imie}" // placeholder renamed
		}
		return o
	}}
	tr := &translator.Translator{LLM: llm, Model: "m"}
	res, err := tr.RunUI(context.Background(), translator.UIOptions{SourcePath: uiSource(t), Version: "bad", OutDir: out, Publish: true, Store: st})
	if err == nil {
		t.Fatal("must fail when one locale fails")
	}
	failed := 0
	for _, r := range res {
		if r.Status == translator.Failed {
			failed++
			if r.Locale != "pl" {
				t.Errorf("unexpected failure: %v", r)
			}
		}
	}
	if failed != 1 {
		t.Fatalf("failed: %d", failed)
	}
	if _, err := os.Stat(filepath.Join(out, "bad", "manifest.json")); err == nil {
		t.Fatal("no manifest on failure")
	}
	if ok, _ := st.Exists(context.Background(), "i18n/manifest.json"); ok {
		t.Fatal("nothing may be published on failure")
	}
	// a partial locale list can never produce a (broken) release
	if _, err := (&translator.Translator{LLM: &fakeLLM{}, Model: "m"}).RunUI(context.Background(), translator.UIOptions{SourcePath: uiSource(t), Version: "part", Targets: []string{"de"}, OutDir: out}); err == nil {
		t.Fatal("incomplete release must be refused")
	}
}

func TestValidateChunkTildeFences(t *testing.T) {
	src := "Read the manual first.\n\n~~~\ncode()\n~~~\n\nThen continue."
	ok := "Lies zuerst das Handbuch.\n\n~~~\ncode()\n~~~\n\nDann weitermachen."
	if err := translator.ValidateChunk(src, ok, "de"); err != nil {
		t.Fatalf("good translation rejected: %v", err)
	}
	for name, dst := range map[string]string{
		"changed code": strings.Replace(ok, "code()", "kode()", 1),
		"lost fence":   strings.Replace(ok, "~~~\ncode()\n~~~", "code()", 1),
	} {
		if translator.ValidateChunk(src, dst, "de") == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}

func TestTranslateStringsSplitsLongValues(t *testing.T) {
	var long strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&long, "## Section %d\n\n%s\n\n", i, strings.Repeat("Words in a paragraph. ", 30))
	}
	llm := &fakeLLM{}
	tr := &translator.Translator{LLM: llm, Model: "m", MaxChunk: 1000}
	out, err := tr.TranslateStrings(context.Background(), "en", "de", map[string]string{"short": "Hello there, world", "rules": strings.TrimSpace(long.String())})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out["rules"], "Section"); got != 12 {
		t.Fatalf("sections: %d", got)
	}
	if llm.calls.Load() < 4 {
		t.Fatalf("a long value must be sent in several requests, got %d calls", llm.calls.Load())
	}
	if out["short"] == "" {
		t.Fatal("short value lost")
	}
}
