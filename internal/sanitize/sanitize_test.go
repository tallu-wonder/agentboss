package sanitize

import (
	"strings"
	"testing"
)

func TestLineStripsTerminalControlSequences(t *testing.T) {
	cases := map[string]string{
		// The attacks this exists to stop: a transcript-supplied name that
		// clears the screen, retitles the window, or writes the clipboard.
		"\x1b[2Jwiped":                   "[2Jwiped",
		"\x1b]0;fake title\x07real":      "]0;fake titlereal",
		"\x1b]52;c;cGF5bG9hZA==\x07copy": "]52;c;cGF5bG9hZA==copy",
		"bell\x07":                       "bell",
		"back\x08space":                  "backspace",
		"carriage\rreturn":               "carriage return",
		"two\nlines":                     "two lines",
		"tab\tseparated":                 "tab separated",
		"null\x00byte":                   "nullbyte",
		"  padded  ":                     "padded",
		"collapse   inner    spaces":     "collapse inner spaces",
		"plain name":                     "plain name",
		"unicode ✓ é 日本":                 "unicode ✓ é 日本",
		"":                               "",
	}
	for in, want := range cases {
		if got := Line(in); got != want {
			t.Errorf("Line(%q) = %q, want %q", in, got, want)
		}
	}
}

// No ESC byte may survive, whatever shape the sequence had: that is what makes
// the result safe to hand to a terminal or to tmux.
func TestLineLeavesNoEscapeBytes(t *testing.T) {
	for _, in := range []string{
		"\x1b[31mred\x1b[0m", "\x1b(B\x1b[m", "\x9b31m", "a\x1bb", "\x1b_priv\x9c",
	} {
		got := Line(in)
		if strings.ContainsAny(got, "\x1b\x07\x00\r\n") {
			t.Errorf("Line(%q) = %q, still contains a control byte", in, got)
		}
		for _, r := range got {
			if r == 0x9b || r == 0x9c { // C1 CSI / ST
				t.Errorf("Line(%q) = %q, kept a C1 control", in, got)
			}
		}
	}
}

func TestLineCapsLength(t *testing.T) {
	got := Line(strings.Repeat("a", maxLen*2))
	if len(got) != maxLen {
		t.Errorf("length = %d, want %d", len(got), maxLen)
	}
	// A cap must not split a multi-byte rune.
	got = Line(strings.Repeat("é", maxLen))
	if !isRuneStart(got[len(got)-1]) && len(got) > 0 {
		// last byte may be a continuation byte of a complete rune; decode check:
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("truncation split a rune")
		}
	}
}
