package translator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"beeloy.com/backend/internal/contentsync"
	"beeloy.com/backend/internal/locales"
	"beeloy.com/backend/internal/platform/s3store"
	"github.com/google/uuid"
)

var keyRe = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)*$`)

func parseUUID(s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(s)
	return id, err == nil
}

// UIOptions control RunUI.
type UIOptions struct {
	SourcePath string
	Version    string
	Targets    []string // empty = all locales except the source one
	OutDir     string
	Publish    bool
	Store      s3store.ObjectStore // required for Publish
}

// RunUI translates the source dictionary into every locale and writes a complete release (files + manifest) to OutDir.
// With Publish it uploads the files to S3 and writes i18n/manifest.json last. A failed locale means no manifest at all.
func (t *Translator) RunUI(ctx context.Context, o UIOptions) ([]LocaleResult, error) {
	raw, err := os.ReadFile(o.SourcePath)
	if err != nil {
		return nil, err
	}
	var src map[string]string
	if err := json.Unmarshal(raw, &src); err != nil || len(src) == 0 {
		return nil, fmt.Errorf("source must be a non-empty flat JSON object of strings")
	}
	for k := range src {
		if !keyRe.MatchString(k) {
			return nil, fmt.Errorf("bad key %q", k)
		}
	}
	ref := locales.Source()
	files := map[string][]byte{}
	enc := func(m map[string]string) []byte { b, _ := json.Marshal(m); return b }
	files[ref] = enc(src)
	targets := o.Targets
	if len(targets) == 0 {
		targets = locales.Codes()
	}
	var results []LocaleResult
	failed := false
	for _, loc := range targets {
		if loc == ref || !locales.Known(loc) {
			continue
		}
		t.logf("translating dictionary -> %s", loc)
		m, err := t.TranslateStrings(ctx, ref, loc, src)
		if err != nil {
			results = append(results, LocaleResult{loc, Failed, err.Error()})
			failed = true
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			continue
		}
		files[loc] = enc(m)
		results = append(results, LocaleResult{loc, Translated, ""})
	}
	if failed {
		return results, fmt.Errorf("some locales failed: no manifest was written")
	}
	for _, c := range locales.Codes() {
		if _, ok := files[c]; !ok {
			return results, fmt.Errorf("incomplete release: %s was not requested (a release must contain all %d languages)", c, len(locales.Codes()))
		}
	}
	m := contentsync.Manifest{SchemaVersion: 1, Version: o.Version, PublishedAt: time.Now().UTC(), ReferenceLocale: ref}
	for _, c := range locales.Codes() {
		sum := sha256.Sum256(files[c])
		m.Locales = append(m.Locales, contentsync.ManifestItem{Code: c, File: fmt.Sprintf("releases/%s/%s.json", o.Version, c), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(files[c]))})
	}
	if err := contentsync.ValidateManifest(m, locales.Codes()); err != nil {
		return results, err
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if o.OutDir != "" {
		dir := filepath.Join(o.OutDir, o.Version)
		if err := os.MkdirAll(filepath.Join(dir, "releases", o.Version), 0o755); err != nil {
			return results, err
		}
		for _, it := range m.Locales {
			if err := os.WriteFile(filepath.Join(dir, it.File), files[it.Code], 0o644); err != nil {
				return results, err
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o644); err != nil {
			return results, err
		}
	}
	if o.Publish {
		if o.Store == nil {
			return results, fmt.Errorf("publishing needs S3")
		}
		if ok, err := o.Store.Exists(ctx, "i18n/"+m.Locales[0].File); err != nil {
			return results, err
		} else if ok {
			return results, fmt.Errorf("version %s already exists in S3", o.Version)
		}
		for _, it := range m.Locales {
			if err := o.Store.Put(ctx, "i18n/"+it.File, files[it.Code], "application/json"); err != nil {
				return results, err
			}
		}
		if err := o.Store.Put(ctx, "i18n/manifest.json", mb, "application/json"); err != nil { // always last
			return results, err
		}
	}
	return results, nil
}
