package notify

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestModeAndWanted(t *testing.T) {
	cases := []struct {
		env              string
		wantMode         string
		needsYou, finish bool
	}{
		{"", "all", true, true},
		{"all", "all", true, true},
		{"always", "all", true, true},
		{"needsyou", "needsyou", true, false},
		{"needs-you", "needsyou", true, false},
		{"off", "off", false, false},
		{"none", "off", false, false},
		{"  OFF  ", "off", false, false},
		{"gibberish", "all", true, true}, // unknown value must not silence alerts
	}
	for _, c := range cases {
		t.Setenv(EnvMode, c.env)
		if got := Mode(); got != c.wantMode {
			t.Errorf("Mode(%q) = %q, want %q", c.env, got, c.wantMode)
		}
		if got := Wanted(NeedsYou); got != c.needsYou {
			t.Errorf("Wanted(NeedsYou) with %q = %v", c.env, got)
		}
		if got := Wanted(Finished); got != c.finish {
			t.Errorf("Wanted(Finished) with %q = %v", c.env, got)
		}
	}
}

// terminal-notifier's -execute value is handed to a shell, so the session id is
// the one field that could execute something. Ids we generate are safe; refuse
// anything else rather than pasting it into a command.
func TestSafeIDRejectsShellSyntax(t *testing.T) {
	for _, ok := range []string{"s_1f3a99", "adk-2", "A_b-9"} {
		if !safeID.MatchString(ok) {
			t.Errorf("%q should be accepted", ok)
		}
	}
	for _, bad := range []string{
		"s_1; rm -rf ~", "$(whoami)", "`id`", "a b", "a|b", "a&b", "a>b", "'x'", `"x"`, "", strings.Repeat("a", 65),
	} {
		if safeID.MatchString(bad) {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

// Session names come from the user and from the agents, so they must not be
// able to terminate the AppleScript string literal they are embedded in.
func TestAppleStrEscapes(t *testing.T) {
	cases := map[string]string{
		`plain`:                   `"plain"`,
		`say "hi"`:                `"say \"hi\""`,
		`back\slash`:              `"back\\slash"`,
		"two\nlines":              `"two lines"`,
		`" & do shell script "id`: `"\" & do shell script \"id"`,
	}
	for in, want := range cases {
		if got := appleStr(in); got != want {
			t.Errorf("appleStr(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/usr/local/bin/agentboss"); got != "'/usr/local/bin/agentboss'" {
		t.Errorf("got %s", got)
	}
	// A path containing a quote must stay one shell word.
	if got := shellQuote("/tmp/it's/agentboss"); got != `'/tmp/it'\''s/agentboss'` {
		t.Errorf("got %s", got)
	}
}

// Detect prefers the backend that supports click-to-focus.
func TestDetectPrefersClickableBackend(t *testing.T) {
	dir := t.TempDir()
	// The fallback is platform-specific: AppleScript on macOS, notify-send
	// elsewhere. Stub whichever one this platform would reach for, or the test
	// only describes the machine it was written on.
	fallback := "notify-send"
	if runtime.GOOS == "darwin" {
		fallback = "osascript"
	}
	for _, name := range []string{"terminal-notifier", fallback} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	b := Detect()
	if !strings.HasSuffix(b.Program, "terminal-notifier") || !b.Clickable {
		t.Fatalf("Detect() = %+v, want the clickable terminal-notifier", b)
	}

	// Without it, we still notify — just without a click action.
	os.Remove(filepath.Join(dir, "terminal-notifier"))
	b = Detect()
	if b.Clickable {
		t.Errorf("the %s backend must not claim click support: %+v", fallback, b)
	}
	if !strings.HasSuffix(b.Program, fallback) {
		t.Errorf("Detect() = %+v, want a fallback to %s", b, fallback)
	}
}

// Send must not block the manager's event loop on the notifier process.
func TestSendDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	stamp := filepath.Join(dir, "ran")
	script := "#!/bin/sh\ntouch " + stamp + "\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "terminal-notifier"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv(EnvMode, "all")

	done := make(chan error, 1)
	go func() {
		done <- Send("/bin/agentboss", Alert{SessionID: "s_1", Name: "n", Kind: NeedsYou})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send blocked on the notifier instead of returning immediately")
	}
}

// A muted desk must not launch the notifier at all.
func TestSendRespectsOff(t *testing.T) {
	dir := t.TempDir()
	stamp := filepath.Join(dir, "ran")
	script := "#!/bin/sh\ntouch " + stamp + "\n"
	if err := os.WriteFile(filepath.Join(dir, "terminal-notifier"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv(EnvMode, "off")
	if err := Send("/bin/agentboss", Alert{SessionID: "s_1", Kind: NeedsYou}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stamp); err == nil {
		t.Fatal("notifier ran even though notifications are off")
	}
}
