package translator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"beeloy.com/backend/internal/articles"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/s3store"
)

const articlePrompt = `You are a professional translator. Translate from {src} to {dst}.
Rules: keep Markdown structure exactly (headings, lists, tables, links, images, emphasis); do not translate code blocks, inline code, URLs, file paths, HTML tags; keep numbers, units and proper names; keep the terms listed in KEEP unchanged; do not add or remove information; do not add comments or explanations.
KEEP: {keep}
Reply with ONE JSON object and nothing else.`

const uiPrompt = articlePrompt + `
Translate only the VALUES; keep every KEY and every {placeholder} unchanged; these are short user-interface strings.`

type Translator struct {
	LLM      Chatter
	Model    string
	Keep     []string
	MaxChunk int // runes per body chunk (default 3000)
	Attempts int // per request (default 3)
	Log      func(format string, args ...any)
}

func (t *Translator) logf(f string, a ...any) {
	if t.Log != nil {
		t.Log(f, a...)
	}
}

func (t *Translator) system(tmpl, src, dst string) string {
	return strings.NewReplacer("{src}", locales.EnglishName(src), "{dst}", locales.EnglishName(dst), "{keep}", strings.Join(t.Keep, ", ")).Replace(tmpl)
}

// ask sends obj as JSON, expects an object of strings back and runs check; it retries, telling the model what was wrong.
func (t *Translator) ask(ctx context.Context, system string, obj map[string]string, check func(map[string]string) error) (map[string]string, error) {
	attempts := t.Attempts
	if attempts == 0 {
		attempts = 3
	}
	user, _ := json.Marshal(obj)
	var reason string
	for i := 1; i <= attempts; i++ {
		sys := system
		if i == attempts && reason != "" && attempts > 1 {
			sys += "\nYour previous answer was invalid: " + reason + "."
		}
		out, err := t.LLM.Chat(ctx, sys, string(user), true)
		if err != nil {
			reason = err.Error()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			reason = "not a JSON object of strings"
			continue
		}
		if err := check(m); err != nil {
			reason = err.Error()
			continue
		}
		return m, nil
	}
	return nil, fmt.Errorf("translation failed after %d attempts: %s", attempts, reason)
}

// TranslateArticle translates title, summary and body from src to dst.
func (t *Translator) TranslateArticle(ctx context.Context, src, dst string, f articles.LocaleFile) (title, summary, body string, err error) {
	system := t.system(articlePrompt, src, dst)
	head, err := t.ask(ctx, system, map[string]string{"title": f.Title, "summary": f.Summary}, func(m map[string]string) error {
		for k, in := range map[string]string{"title": f.Title, "summary": f.Summary} {
			if err := ValidateChunk(in, m[k], dst); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
		return nil
	})
	if err != nil {
		return "", "", "", err
	}
	max := t.MaxChunk
	if max == 0 {
		max = 3000
	}
	chunks, err := SplitMarkdown(f.BodyMD, max)
	if err != nil {
		return "", "", "", err // rejected before the first model call for the body
	}
	var parts []string
	for i, chunk := range chunks {
		t.logf("  chunk %d (%d runes)", i+1, runeLen(chunk))
		out, err := t.ask(ctx, system, map[string]string{"body_md": chunk}, func(m map[string]string) error {
			return ValidateChunk(chunk, m["body_md"], dst)
		})
		if err != nil {
			return "", "", "", fmt.Errorf("body chunk %d: %w", i+1, err)
		}
		parts = append(parts, strings.TrimSpace(out["body_md"]))
	}
	return strings.TrimSpace(head["title"]), strings.TrimSpace(head["summary"]), strings.Join(parts, "\n\n"), nil
}

// TranslateStrings translates a flat dictionary in batches of at most 40 keys / 4000 runes.
// A single value longer than MaxChunk (a legal text, for example) is split like an article body and translated chunk by chunk.
func (t *Translator) TranslateStrings(ctx context.Context, src, dst string, msgs map[string]string) (map[string]string, error) {
	system := t.system(uiPrompt, src, dst)
	max := t.MaxChunk
	if max == 0 {
		max = 3000
	}
	out := map[string]string{}
	keys := make([]string, 0, len(msgs))
	for k, v := range msgs {
		if runeLen(v) <= max {
			keys = append(keys, k)
			continue
		}
		long, err := t.translateLong(ctx, src, dst, v, max)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = long
	}
	sort.Strings(keys)
	var batch []string
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		in := map[string]string{}
		for _, k := range batch {
			in[k] = msgs[k]
		}
		res, err := t.ask(ctx, system, in, func(m map[string]string) error {
			if len(m) != len(in) {
				return fmt.Errorf("the set of keys changed")
			}
			for k, v := range in {
				got, ok := m[k]
				if !ok {
					return fmt.Errorf("key %q is missing", k)
				}
				if strings.TrimSpace(got) == "" {
					return fmt.Errorf("value of %q is empty", k)
				}
				if sortedSet(placeholderRe, v) != sortedSet(placeholderRe, got) {
					return fmt.Errorf("placeholders of %q changed", k)
				}
				if runeLen(v) >= 12 {
					if err := ValidateChunk(v, got, dst); err != nil {
						return fmt.Errorf("%q: %w", k, err)
					}
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for k, v := range res {
			out[k] = v
		}
		batch, size = nil, 0
		return nil
	}
	for _, k := range keys {
		l := runeLen(msgs[k]) + runeLen(k)
		if len(batch) >= 40 || (size+l > 4000 && len(batch) > 0) {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		batch = append(batch, k)
		size += l
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// translateLong translates a long Markdown value chunk by chunk (fenced code and tables are never cut).
func (t *Translator) translateLong(ctx context.Context, src, dst, text string, max int) (string, error) {
	system := t.system(articlePrompt, src, dst)
	chunks, err := SplitMarkdown(text, max)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		t.logf("  chunk %d/%d (%d runes)", i+1, len(chunks), runeLen(chunk))
		res, err := t.ask(ctx, system, map[string]string{"body_md": chunk}, func(m map[string]string) error {
			return ValidateChunk(chunk, m["body_md"], dst)
		})
		if err != nil {
			return "", fmt.Errorf("chunk %d: %w", i+1, err)
		}
		parts = append(parts, strings.TrimSpace(res["body_md"]))
	}
	return strings.Join(parts, "\n\n"), nil
}

// ---- article mode ----

type Status string

const (
	Translated Status = "translated"
	Skipped    Status = "skipped"
	Failed     Status = "failed"
)

type LocaleResult struct {
	Locale string
	Status Status
	Reason string
}

// RunArticle writes machine translations of the latest source revision to S3. It never touches manifest.json,
// never replaces any existing translation (human or machine) and never changes publication state.
func (t *Translator) RunArticle(ctx context.Context, store s3store.ObjectStore, id string, targets []string) ([]LocaleResult, error) {
	aid, ok := parseUUID(id)
	if !ok {
		return nil, fmt.Errorf("bad article id")
	}
	raw, err := store.Get(ctx, articles.ManifestKey(aid), articles.MaxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	var m articles.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if err := articles.ValidateManifest(m, aid, locales.Known); err != nil {
		return nil, err
	}
	if m.Rev < 1 {
		return nil, fmt.Errorf("article has no source text yet")
	}
	srcRaw, err := store.Get(ctx, articles.LocaleKey(aid, m.Rev, m.SourceLocale), articles.MaxLocaleFileBytes)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	var src articles.LocaleFile
	if err := json.Unmarshal(srcRaw, &src); err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	if err := articles.ValidateLocaleFile(src, srcRaw, aid, m.SourceLocale, m.Rev); err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	if len(targets) == 0 {
		for _, c := range locales.Codes() {
			targets = append(targets, c)
		}
	}
	var results []LocaleResult
	for _, loc := range targets {
		if loc == m.SourceLocale || !locales.Known(loc) {
			continue
		}
		key := articles.LocaleKey(aid, m.Rev, loc)
		if exists, err := store.Exists(ctx, key); err != nil {
			return results, err
		} else if exists {
			results = append(results, LocaleResult{loc, Skipped, "already translated; files are immutable (a correction needs a new source revision)"})
			continue
		}
		t.logf("translating %s -> %s", m.SourceLocale, loc)
		title, summary, body, err := t.TranslateArticle(ctx, m.SourceLocale, loc, src)
		if err != nil {
			results = append(results, LocaleResult{loc, Failed, err.Error()})
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			continue
		}
		model := t.Model
		out := articles.LocaleFile{SchemaVersion: 1, ArticleID: aid, Locale: loc, Rev: m.Rev, Origin: "machine", Model: &model, Title: title, Summary: summary, BodyMD: body, GeneratedAt: time.Now().UTC()}
		b, _ := json.Marshal(out)
		if err := articles.ValidateLocaleFile(out, b, aid, loc, m.Rev); err != nil {
			results = append(results, LocaleResult{loc, Failed, "result is not a valid article file: " + err.Error()})
			continue
		}
		if err := store.PutIfAbsent(ctx, key, b, "application/json"); err != nil {
			if errors.Is(err, s3store.ErrConflict) { // someone created it while the model was working: keep theirs
				results = append(results, LocaleResult{loc, Skipped, "appeared concurrently; not replaced"})
				continue
			}
			results = append(results, LocaleResult{loc, Failed, err.Error()})
			continue
		}
		results = append(results, LocaleResult{loc, Translated, ""})
	}
	return results, nil
}
