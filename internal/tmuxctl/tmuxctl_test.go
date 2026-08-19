package tmuxctl

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// isolate points tmux at a private socket so the test never touches the user's
// running server. The directory stays short because tmux socket paths are
// capped near 104 bytes and macOS temp dirs are long.
func isolate(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir, err := os.MkdirTemp("/tmp", "adktest")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-server").Run()
		os.RemoveAll(dir)
	})
	// A server must exist before options can be set.
	if err := exec.Command("tmux", "new-session", "-d", "-s", "probe", "sleep 30").Run(); err != nil {
		t.Skipf("cannot start an isolated tmux server: %v", err)
	}
}

// tmuxVersion is what the running tmux calls itself, for messages.
func tmuxVersion() string {
	out, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// serverOptions lists the server options the running tmux understands.
func serverOptions(t *testing.T) map[string]bool {
	t.Helper()
	out, err := exec.Command("tmux", "show-options", "-s").Output()
	if err != nil {
		return nil
	}
	known := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if name, _, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			known[name] = true
		}
	}
	return known
}

// wantOption asserts a server option's value, skipping the check on a tmux that
// has never heard of it.
func wantOption(t *testing.T, name, want string) {
	t.Helper()
	if known := serverOptions(t); known != nil && !known[name] {
		t.Logf("%s has no %s option — skipping (agentboss degrades rather than failing)",
			tmuxVersion(), name)
		return
	}
	if got := show(t, "show-options", "-sv", name); got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func show(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		t.Fatalf("tmux %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// Shift+enter must insert a newline rather than submit the prompt. Codex asks
// for modified keys with the kitty protocol, which tmux does not recognize, so
// no extended-keys encoding reaches it — the binding that rewrites the chord to
// ESC CR is what actually makes it work, and must not be dropped.
func TestConfigureServerMakesShiftEnterANewline(t *testing.T) {
	isolate(t)
	ConfigureServer()

	// Older tmux builds lack some of these options. ConfigureServer ignores a
	// rejected set-option by design — the S-Enter binding below is what actually
	// delivers the newline — so the assertions apply only where the running tmux
	// knows the option, and say so rather than failing on an old build.
	wantOption(t, "extended-keys", "on")
	wantOption(t, "extended-keys-format", "csi-u")

	// A pane copy has to survive the nested viewport: tmux's default
	// ("external") drops clipboard writes from applications, and the inner
	// client is an application.
	wantOption(t, "set-clipboard", "on")

	keys := show(t, "list-keys", "-T", "root")
	var bind string
	for _, l := range strings.Split(keys, "\n") {
		if strings.Contains(l, "S-Enter") {
			bind = l
		}
	}
	if bind == "" {
		t.Fatal("no root binding for S-Enter: shift+enter would submit the prompt in Codex")
	}
	// ESC CR, the sequence both agents accept as a newline.
	if !strings.Contains(bind, "1b 0d") {
		t.Errorf("S-Enter must send ESC CR, got: %s", bind)
	}
	// Other tmux sessions keep tmux's own behavior.
	if !strings.Contains(bind, ManagerSession) {
		t.Errorf("binding should be scoped to the %s session, got: %s", ManagerSession, bind)
	}
}
