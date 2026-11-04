package translator

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	urlRe         = regexp.MustCompile(`https?://[^\s)\]>"']+`)
	fenceRe       = regexp.MustCompile("(?s)```.*?```")
	headingRe     = regexp.MustCompile(`(?m)^#{1,6} `)
	placeholderRe = regexp.MustCompile(`\{[a-zA-Z_][a-zA-Z0-9_]*\}`)
)

func sortedSet(re *regexp.Regexp, s string) string {
	m := re.FindAllString(s, -1)
	sort.Strings(m)
	return strings.Join(m, "\x00")
}

// ValidateChunk checks that dst is a faithful translation of src: URLs, code blocks, placeholders and headings are
// preserved, the length is plausible and the text was really translated. dstLocale selects the length range.
func ValidateChunk(src, dst, dstLocale string) error {
	if strings.TrimSpace(dst) == "" {
		return fmt.Errorf("empty translation")
	}
	if sortedSet(urlRe, src) != sortedSet(urlRe, dst) {
		return fmt.Errorf("URLs differ from the source")
	}
	if strings.Count(src, "```") != strings.Count(dst, "```") {
		return fmt.Errorf("number of code fences differs")
	}
	if fenceBlocks(src) != fenceBlocks(dst) {
		return fmt.Errorf("code blocks were changed")
	}
	if sortedSet(placeholderRe, src) != sortedSet(placeholderRe, dst) {
		return fmt.Errorf("placeholders differ from the source")
	}
	if len(headingRe.FindAllString(src, -1)) != len(headingRe.FindAllString(dst, -1)) {
		return fmt.Errorf("number of headings differs")
	}
	sl, dl := float64(runeLen(src)), float64(runeLen(dst))
	if sl > 0 {
		min, max := 0.3, 3.5
		switch dstLocale {
		case "zh", "ja", "ko":
			min = 0.1
		}
		if r := dl / sl; r < min || r > max {
			return fmt.Errorf("suspicious length ratio %.2f", r)
		}
	}
	if sl > 40 && strings.TrimSpace(src) == strings.TrimSpace(dst) {
		return fmt.Errorf("the text was not translated")
	}
	return nil
}

func fenceBlocks(s string) string { return strings.Join(fenceRe.FindAllString(s, -1), "\x00") }
