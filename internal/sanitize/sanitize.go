// Package sanitize cleans text that agentdeck did not write itself.
//
// Session names, summaries, todo items, model ids and hook messages all
// originate outside the desk — from transcripts an agent wrote, from a prompt
// someone else supplied, or from a hand-edited state file — and every one of
// them ends up rendered into a terminal or handed to tmux. Untouched, a name
// containing escape sequences could clear the screen, rewrite the window title,
// drive OSC 52 to put data on the clipboard, or simply break the sidebar's
// layout with a newline. Text is therefore reduced to printable characters
// before it is stored or shown.
package sanitize

import (
	"strings"
	"unicode"
)

// maxLen bounds a single field. Nothing legitimate needs more, and an
// unbounded name from a transcript would otherwise flood the sidebar, the tab
// bar and every notification built from it.
const maxLen = 512

// Line returns s as a single line of printable text: control characters
// (including ESC, CR and LF) and other non-printable runes are dropped, tabs
// and newlines become spaces, runs of whitespace collapse, and the result is
// trimmed and length-capped.
func Line(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r' || unicode.IsSpace(r):
			space = true
		case r == 0x7f || unicode.IsControl(r):
			// ESC and friends: drop entirely rather than substitute, so an
			// escape sequence cannot be reassembled from what is left.
		case !unicode.IsPrint(r):
			// Unassigned code points, unpaired surrogates, and the like.
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > maxLen {
		out = strings.TrimSpace(truncateRunes(out, maxLen))
	}
	return out
}

// truncateRunes cuts to at most n bytes without splitting a rune.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
