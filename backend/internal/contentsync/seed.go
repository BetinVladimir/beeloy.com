package contentsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"beeloy.com/backend/internal/platform/s3store"
)

// SeedVersion is the version of the template release: the reference text copied into every locale.
const SeedVersion = "seed-template"

// SeedTemplate publishes a placeholder release (the reference dictionary under every required locale) when the bucket
// has no i18n/manifest.json yet, so that a fresh environment starts with a complete, valid release. It never overwrites:
// files and manifest are created with PutIfAbsent, the manifest last. Real translations are published later by the
// translator under a new version. Returns false if a manifest already exists.
func SeedTemplate(ctx context.Context, st s3store.ObjectStore, reference []byte, referenceLocale string, required []string) (bool, error) {
	if ok, err := st.Exists(ctx, manifestKey); err != nil || ok {
		return false, err
	}
	var dict map[string]string
	if err := json.Unmarshal(reference, &dict); err != nil || len(dict) == 0 {
		return false, errors.New("seed: reference dictionary must be a non-empty flat JSON object")
	}
	m := Manifest{SchemaVersion: 1, Version: SeedVersion, PublishedAt: time.Now().UTC(), ReferenceLocale: referenceLocale}
	sum := sha256.Sum256(reference)
	for _, c := range required {
		file := fmt.Sprintf("releases/%s/%s.json", SeedVersion, c)
		if err := st.PutIfAbsent(ctx, uiPrefix+file, reference, "application/json"); err != nil && !errors.Is(err, s3store.ErrConflict) {
			return false, err
		}
		m.Locales = append(m.Locales, ManifestItem{Code: c, File: file, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(reference))})
	}
	if err := ValidateManifest(m, required); err != nil {
		return false, err
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := st.PutIfAbsent(ctx, manifestKey, mb, "application/json"); err != nil {
		if errors.Is(err, s3store.ErrConflict) {
			return false, nil // someone published a real release meanwhile
		}
		return false, err
	}
	return true, nil
}
