package translator

import (
	"errors"
	"regexp"
	"strings"
)

// ErrChunkTooLarge is returned before any model call when a fenced code block or a table exceeds the chunk limit:
// such blocks are never cut.
var ErrChunkTooLarge = errors.New("chunk_too_large: a code block or table is larger than the chunk limit")

var chunkFenceRe = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")

type blockKind int

const (
	kindText blockKind = iota
	kindFence
	kindTable
)

type block struct {
	text string
	kind blockKind
}

// SplitMarkdown splits text into chunks of at most maxRunes runes. Fenced code (``` or ~~~, any length) and tables
// are indivisible; an ordinary paragraph that is too long is cut by lines, and a too long line at word boundaries.
// Joining the chunks with "\n\n" restores the text (up to blank-line normalisation).
func SplitMarkdown(s string, maxRunes int) ([]string, error) {
	if maxRunes <= 0 {
		maxRunes = 3000
	}
	var blocks []block
	var cur []string
	kind := kindText
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, block{strings.Join(cur, "\n"), kind})
			cur, kind = nil, kindText
		}
	}
	fenceMark := ""
	for _, ln := range strings.Split(s, "\n") {
		if fenceMark != "" {
			cur = append(cur, ln)
			// closing fence: same character, at least as long, nothing else on the line
			t := strings.TrimSpace(ln)
			if strings.HasPrefix(t, fenceMark) && strings.Trim(t, fenceMark[:1]) == "" {
				flush()
				fenceMark = ""
			}
			continue
		}
		if m := chunkFenceRe.FindStringSubmatch(ln); m != nil {
			flush()
			fenceMark, kind = m[1], kindFence
			cur = append(cur, ln)
			continue
		}
		if strings.TrimSpace(ln) == "" {
			flush()
			continue
		}
		isTable := strings.HasPrefix(strings.TrimSpace(ln), "|")
		if isTable && kind != kindTable {
			flush()
			kind = kindTable
		} else if !isTable && kind == kindTable {
			flush()
		}
		cur = append(cur, ln)
	}
	flush() // an unterminated fence stays one indivisible block

	var parts []string
	for _, b := range blocks {
		if runeLen(b.text) <= maxRunes {
			parts = append(parts, b.text)
			continue
		}
		if b.kind != kindText {
			return nil, ErrChunkTooLarge
		}
		var buf []string
		size := 0
		emit := func() {
			if len(buf) > 0 {
				parts = append(parts, strings.Join(buf, "\n"))
				buf, size = nil, 0
			}
		}
		for _, ln := range strings.Split(b.text, "\n") {
			for _, piece := range cutLine(ln, maxRunes) {
				l := runeLen(piece) + 1
				if size+l > maxRunes {
					emit()
				}
				buf = append(buf, piece)
				size += l
			}
		}
		emit()
	}
	var chunks []string
	var acc []string
	size := 0
	for _, p := range parts {
		l := runeLen(p) + 2
		if size+l > maxRunes && len(acc) > 0 {
			chunks = append(chunks, strings.Join(acc, "\n\n"))
			acc, size = nil, 0
		}
		acc = append(acc, p)
		size += l
	}
	if len(acc) > 0 {
		chunks = append(chunks, strings.Join(acc, "\n\n"))
	}
	return chunks, nil
}

// cutLine splits a line longer than max at spaces, keeping the separators inside the pieces.
func cutLine(ln string, max int) []string {
	if runeLen(ln) <= max {
		return []string{ln}
	}
	var out []string
	r := []rune(ln)
	for len(r) > max {
		cut := max
		for i := max; i > max/2; i-- {
			if r[i-1] == ' ' {
				cut = i
				break
			}
		}
		out = append(out, string(r[:cut]))
		r = r[cut:]
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}

func runeLen(s string) int { return len([]rune(s)) }
