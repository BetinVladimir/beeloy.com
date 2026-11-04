package contentsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	versionRe     = regexp.MustCompile(`^[0-9A-Za-z._-]{1,64}$`)
	keyRe         = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)*$`)
	placeholderRe = regexp.MustCompile(`\{[a-zA-Z_][a-zA-Z0-9_]*\}`)
)

// ValidateManifest checks the structure of the manifest against the set of required locales.
func ValidateManifest(m Manifest, required []string) error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("manifest: unsupported schema_version %d", m.SchemaVersion)
	}
	if !versionRe.MatchString(m.Version) {
		return fmt.Errorf("manifest: bad version %q", m.Version)
	}
	req := map[string]bool{}
	for _, c := range required {
		req[c] = true
	}
	if !req[m.ReferenceLocale] {
		return fmt.Errorf("manifest: reference_locale %q is not a supported locale", m.ReferenceLocale)
	}
	seen := map[string]bool{}
	var total int64
	for _, it := range m.Locales {
		switch {
		case !req[it.Code]:
			return fmt.Errorf("manifest: unexpected locale %q", it.Code)
		case seen[it.Code]:
			return fmt.Errorf("manifest: duplicate locale %q", it.Code)
		case it.File == "" || strings.HasPrefix(it.File, "/") || strings.Contains(it.File, ".."):
			return fmt.Errorf("manifest: bad file path for %q", it.Code)
		case it.Size <= 0 || it.Size > MaxFileSize:
			return fmt.Errorf("manifest: bad size for %q", it.Code)
		case len(it.SHA256) != 64:
			return fmt.Errorf("manifest: bad sha256 for %q", it.Code)
		}
		seen[it.Code] = true
		total += it.Size
	}
	for _, c := range required {
		if !seen[c] {
			return fmt.Errorf("manifest: locale %q is missing", c)
		}
	}
	if total > MaxTotalSize {
		return fmt.Errorf("manifest: release is larger than %d bytes", MaxTotalSize)
	}
	return nil
}

// ValidateBundle checks size, checksum and content rules of one dictionary file.
func ValidateBundle(item ManifestItem, raw []byte) (Bundle, error) {
	if int64(len(raw)) != item.Size {
		return Bundle{}, fmt.Errorf("%s: size %d != manifest %d", item.Code, len(raw), item.Size)
	}
	sum := sha256.Sum256(raw)
	h := hex.EncodeToString(sum[:])
	if !strings.EqualFold(h, item.SHA256) {
		return Bundle{}, fmt.Errorf("%s: sha256 mismatch", item.Code)
	}
	var m map[string]string
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(&m); err != nil || m == nil {
		return Bundle{}, fmt.Errorf("%s: must be a flat JSON object of strings", item.Code)
	}
	for k, v := range m {
		if !keyRe.MatchString(k) {
			return Bundle{}, fmt.Errorf("%s: bad key %q", item.Code, k)
		}
		if utf8.RuneCountInString(v) > 5000 {
			return Bundle{}, fmt.Errorf("%s: value of %q is too long", item.Code, k)
		}
	}
	return Bundle{Locale: item.Code, Raw: raw, Messages: m, SHA256: h}, nil
}

func placeholders(s string) []string {
	p := placeholderRe.FindAllString(s, -1)
	sort.Strings(p)
	return p
}

// ValidateSet compares every dictionary with the reference one: same keys, same placeholders per key.
// It returns all problems (at most 50).
func ValidateSet(m Manifest, bundles map[string]Bundle) []string {
	var problems []string
	add := func(f string, a ...any) {
		if len(problems) < maxProblems {
			problems = append(problems, fmt.Sprintf(f, a...))
		}
	}
	ref, ok := bundles[m.ReferenceLocale]
	if !ok {
		return []string{"reference locale " + m.ReferenceLocale + " is missing"}
	}
	codes := make([]string, 0, len(bundles))
	for c := range bundles {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, code := range codes {
		if code == m.ReferenceLocale {
			continue
		}
		b := bundles[code]
		for k := range ref.Messages {
			v, has := b.Messages[k]
			if !has {
				add("%s: missing key %q", code, k)
				continue
			}
			if strings.Join(placeholders(ref.Messages[k]), ",") != strings.Join(placeholders(v), ",") {
				add("%s: placeholders of %q differ from the reference", code, k)
			}
		}
		for k := range b.Messages {
			if _, has := ref.Messages[k]; !has {
				add("%s: extra key %q", code, k)
			}
		}
	}
	return problems
}
