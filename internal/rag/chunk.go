package rag

import (
	"strings"
	"unicode/utf8"
)

// ChunkText splits into ~800-char chunks with ~150 overlap at whitespace boundaries.
func ChunkText(text string, size, overlap int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var out []string
	runes := []rune(text)
	start := 0
	for start < len(runes) {
		end := start + size
		if end >= len(runes) {
			out = append(out, strings.TrimSpace(string(runes[start:])))
			break
		}
		// backtrack to whitespace
		cut := end
		for cut > start && cut > end-100 && runes[cut] != ' ' && runes[cut] != '\n' {
			cut--
		}
		if cut <= start {
			cut = end
		}
		out = append(out, strings.TrimSpace(string(runes[start:cut])))
		start = cut - overlap
		if start < 0 {
			start = 0
		}
		if _ = utf8.RuneCountInString(text); start >= len(runes) {
			break
		}
	}
	return out
}

// BuildContext formats retrieved chunks as citable data block.
func BuildContext(filenames, contents []string, idx []int) string {
	if len(contents) == 0 {
		return "(no chunks)"
	}
	var b strings.Builder
	for i := range contents {
		fn := "doc"
		if i < len(filenames) {
			fn = filenames[i]
		}
		ix := i
		if i < len(idx) {
			ix = idx[i]
		}
		b.WriteString("[" + fn + " §" + itoa(ix) + "]\n" + contents[i] + "\n\n")
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	p := len(b)
	for n > 0 {
		p--
		b[p] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
