//go:build e2e

// Package e2e drives a real agentboss manager inside a real tmux server.
//
// The unit tests cover pieces in isolation; these cover the thing that actually
// breaks — the desk as a whole. Every regression this suite exists for was found
// by hand first: a viewport that stopped recognizing its own session after a
// rename, digit keys that addressed dormant sessions, a confirmation nobody
// could see, a menu that closed before it could be used. All of them are
// invisible to unit tests and obvious the moment a session is driven end to end.
//
// Run with: go test -tags e2e ./internal/e2e/
//
// The suite is behind a build tag because it needs tmux, spawns processes, and
// takes seconds rather than milliseconds. It never touches the developer's own
// desk: a private tmux socket, a temporary AGENTBOSS_HOME, stub agents, and a
// stub notifier and file-opener that record what they were asked to do.
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// desk is one isolated agentboss under test.
type desk struct {
	t        *testing.T
	home     string // AGENTBOSS_HOME
	bin      string // the agentboss binary under test
	dir      string // sandbox root
	socket   string // TMUX_TMPDIR for the private server
	notifLog string
	openLog  string
}

// newDesk builds agentboss, lays out a sandbox, and starts the manager.
func newDesk(t *testing.T) *desk {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	// tmux sockets are unix paths with a ~104 byte limit, and macOS temp dirs
	// are long, so the socket lives somewhere short.
	socket, err := os.MkdirTemp("/tmp", "adke2e")
	if err != nil {
		t.Fatal(err)
	}
	d := &desk{
		t:        t,
		dir:      socket,
		socket:   socket,
		home:     filepath.Join(socket, "home"),
		bin:      filepath.Join(socket, "agentboss"),
		notifLog: filepath.Join(socket, "notifications.log"),
		openLog:  filepath.Join(socket, "opened.log"),
	}
	for _, p := range []string{d.home, filepath.Join(socket, "bin"), filepath.Join(socket, "work")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command("go", "build", "-o", d.bin, ".")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building agentboss: %v\n%s", err, out)
	}
	d.writeStub("fakeagent", "#!/bin/bash\nexec cat\n")
	d.writeStub("terminal-notifier", "#!/bin/bash\n{ for a in \"$@\"; do printf '%s\\n' \"$a\"; done; echo ---; } >> "+d.notifLog+"\n")
	d.writeStub("open", "#!/bin/bash\necho \"$@\" >> "+d.openLog+"\n")
	t.Cleanup(d.stop)
	d.start()
	return d
}

func (d *desk) writeStub(name, body string) {
	d.t.Helper()
	p := filepath.Join(d.dir, "bin", name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		d.t.Fatal(err)
	}
}

// tmux runs a tmux command against the private server.
func (d *desk) tmux(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	cmd.Env = append(os.Environ(), "TMUX_TMPDIR="+d.socket)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (d *desk) start() {
	d.t.Helper()
	env := []string{
		"PATH=" + filepath.Join(d.dir, "bin") + ":" + os.Getenv("PATH"),
		"TMUX_TMPDIR=" + d.socket,
		"TERM=xterm-256color",
		"TERM_PROGRAM=ghostty",
		"HOME=" + d.dir,
		"AGENTBOSS_HOME=" + d.home,
		"AGENTBOSS_CLAUDE_SETTINGS=" + filepath.Join(d.dir, "settings.json"),
		"AGENTBOSS_CLAUDE_CMD=" + filepath.Join(d.dir, "bin", "fakeagent"),
		"AGENTBOSS_CLAUDE_PROJECTS=" + filepath.Join(d.dir, "projects"),
		"AGENTBOSS_OPEN_CMD=" + filepath.Join(d.dir, "bin", "open"),
	}
	cmd := exec.Command("tmux", "new-session", "-d", "-s", "agentboss", "-x", "150", "-y", "32", d.bin+" __ui")
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		d.t.Fatalf("starting the manager: %v\n%s", err, out)
	}
	// The sidebar is up once it has drawn its header.
	d.waitFor("the sidebar to draw", func() bool { return strings.Contains(d.sidebar(), "agentboss") })
}

func (d *desk) stop() {
	_, _ = d.tmux("kill-server")
	os.RemoveAll(d.socket)
}

// keys sends keystrokes to the sidebar.
func (d *desk) keys(keys ...string) {
	d.t.Helper()
	for _, k := range keys {
		if _, err := d.tmux("send-keys", "-t", "agentboss:0.0", k); err != nil {
			d.t.Fatalf("send-keys %q: %v", k, err)
		}
		time.Sleep(120 * time.Millisecond)
	}
}

// literal types text without interpreting it as key names.
func (d *desk) literal(text string) {
	d.t.Helper()
	if _, err := d.tmux("send-keys", "-t", "agentboss:0.0", "-l", "--", text); err != nil {
		d.t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
}

// sidebar is the manager pane's visible text.
func (d *desk) sidebar() string {
	out, _ := d.tmux("capture-pane", "-p", "-t", "agentboss:0.0")
	return out
}

// tabs is the tab bar as tmux stores it, styling stripped.
func (d *desk) tabs() string {
	out, _ := d.tmux("show-options", "-t", "=agentboss:", "-v", "status-format[0]")
	for {
		i := strings.Index(out, "#[")
		if i < 0 {
			break
		}
		j := strings.Index(out[i:], "]")
		if j < 0 {
			break
		}
		out = out[:i] + out[i+j+1:]
	}
	return out
}

// liveSessions lists the agent tmux sessions currently running.
func (d *desk) liveSessions() []string {
	out, _ := d.tmux("list-sessions", "-F", "#{session_name}")
	var live []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "adk_") {
			live = append(live, strings.TrimPrefix(l, "adk_"))
		}
	}
	return live
}

// deskState is the persisted desk.
type deskState struct {
	Sessions []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Dir      string `json:"dir"`
		Archived bool   `json:"archived"`
	} `json:"sessions"`
	NotifyMuted bool `json:"notify_muted"`
}

func (d *desk) state() deskState {
	d.t.Helper()
	var st deskState
	data, err := os.ReadFile(filepath.Join(d.home, "state.json"))
	if err != nil {
		return st
	}
	if err := json.Unmarshal(data, &st); err != nil {
		d.t.Fatalf("state.json is not valid JSON: %v", err)
	}
	return st
}

// newSession runs the new-session wizard for a directory under the sandbox.
func (d *desk) newSession(name string) string {
	d.t.Helper()
	dir := filepath.Join(d.dir, "work", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		d.t.Fatal(err)
	}
	before := len(d.state().Sessions)
	d.keys("M-n")
	d.keys("C-u")
	d.literal(dir)
	d.keys("Enter") // folder
	d.keys("Enter") // agent picker: Claude
	d.waitFor("the session to appear on the desk", func() bool {
		return len(d.state().Sessions) > before
	})
	sessions := d.state().Sessions
	return sessions[len(sessions)-1].ID
}

// hook feeds an agent status event, exactly as Claude Code's hooks do.
func (d *desk) hook(id, payload string) {
	d.t.Helper()
	cmd := exec.Command(d.bin, "hook")
	cmd.Env = append(os.Environ(), "AGENTBOSS_HOME="+d.home, "AGENTBOSS_ID="+id,
		"TMUX_TMPDIR="+d.socket)
	cmd.Stdin = strings.NewReader(payload)
	if out, err := cmd.CombinedOutput(); err != nil {
		d.t.Fatalf("hook: %v\n%s", err, out)
	}
}

// notifications counts what the stub notifier was asked to post.
func (d *desk) notifications() int {
	data, err := os.ReadFile(d.notifLog)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "---")
}

// waitFor polls until cond holds, failing the test on timeout. Polling beats
// sleeping: the manager ticks a few times a second and machines differ.
func (d *desk) waitFor(what string, cond func() bool) {
	d.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	d.t.Fatalf("timed out waiting for %s\nsidebar:\n%s", what, d.sidebar())
}

// ---- the tests ---------------------------------------------------------

// A new session must appear on the desk, run under tmux, and get a tab.
func TestNewSessionRunsAndGetsATab(t *testing.T) {
	d := newDesk(t)
	id := d.newSession("alpha")

	d.waitFor("the agent to start", func() bool {
		for _, s := range d.liveSessions() {
			if s == id {
				return true
			}
		}
		return false
	})
	d.waitFor("a tab for it", func() bool { return strings.Contains(d.tabs(), "alpha") })
	if !strings.Contains(d.sidebar(), "alpha") {
		t.Errorf("the session is missing from the sidebar:\n%s", d.sidebar())
	}
}

// The digit keys address open sessions in tab order, so a dormant session must
// not consume a number.
func TestDigitKeysFollowTheTabs(t *testing.T) {
	d := newDesk(t)
	first := d.newSession("first")
	d.keys("C-\\") // back to the sidebar
	second := d.newSession("second")
	d.keys("C-\\")

	// Close the first session's tab: it stays on the desk but stops being open.
	d.waitFor("both to be open", func() bool { return len(d.liveSessions()) == 2 })
	d.keys("Home")
	d.keys("M-z", "y") // close the tab of the selected (first) session; z asks first
	d.waitFor("the first tab to close", func() bool {
		for _, s := range d.liveSessions() {
			if s == first {
				return false
			}
		}
		return true
	})

	// Only one session is open now, so 1 must address it — the second one.
	if got := d.sidebar(); !strings.Contains(got, "1 ") {
		t.Errorf("expected a badge for the one open session:\n%s", got)
	}
	d.keys("M-1")
	d.waitFor("the viewport to hold the open session", func() bool {
		out, _ := d.tmux("list-clients", "-F", "#{client_session}")
		return strings.Contains(out, "adk_"+second)
	})
}

// Reviving a shelved session must ask first, and must not start anything when
// the answer is no.
func TestShelvedSessionAsksBeforeWaking(t *testing.T) {
	d := newDesk(t)
	id := d.newSession("shelved")
	d.keys("C-\\")
	d.waitFor("it to be open", func() bool { return len(d.liveSessions()) == 1 })

	d.keys("Home")
	d.keys("M-x") // close → old
	d.waitFor("the confirmation to appear", func() bool {
		return strings.Contains(d.sidebar(), "old?")
	})
	d.keys("y")
	d.waitFor("it to land on the shelf", func() bool {
		for _, s := range d.state().Sessions {
			if s.ID == id {
				return s.Archived
			}
		}
		return false
	})
	// Shelving also stops the agent, and that happens a moment after the flag
	// is written. An archived session that is STILL running opens without
	// asking, since there is nothing to wake — so wait for it to be gone or
	// this tests the wrong branch.
	d.waitFor("the agent to stop", func() bool { return len(d.liveSessions()) == 0 })

	// The `old` section starts collapsed, so open it before the session can be
	// selected at all.
	d.keys("End")
	d.keys("M-Space")
	d.waitFor("the shelf to open", func() bool { return strings.Contains(d.sidebar(), "shelved") })

	// Opening it asks rather than waking it.
	d.keys("End")
	d.keys("Enter")
	d.waitFor("the revive prompt", func() bool { return strings.Contains(d.sidebar(), "revive") })
	d.keys("M-n")
	time.Sleep(700 * time.Millisecond)
	if len(d.liveSessions()) != 0 {
		t.Errorf("answering no started an agent anyway: %v", d.liveSessions())
	}
	for _, s := range d.state().Sessions {
		if s.ID == id && !s.Archived {
			t.Error("answering no took the session off the shelf")
		}
	}

	// Saying yes revives it.
	d.keys("Enter")
	d.waitFor("the revive prompt again", func() bool { return strings.Contains(d.sidebar(), "revive") })
	d.keys("y")
	d.waitFor("the session to come back", func() bool { return len(d.liveSessions()) == 1 })
}

// A confirmation has to be visible: it is drawn as a box, not tucked into the
// footer where it gets pressed past.
func TestConfirmationIsAPopup(t *testing.T) {
	d := newDesk(t)
	d.newSession("doomed")
	d.keys("C-\\")
	d.keys("Home")
	d.keys("M-x")
	d.waitFor("the confirmation box", func() bool {
		s := d.sidebar()
		return strings.Contains(s, "╭") && strings.Contains(s, "old?") && strings.Contains(s, "y confirm")
	})
	d.keys("M-n")
}

// An alert on a session you are not looking at notifies; muting silences it.
func TestNotifiesAndMutes(t *testing.T) {
	d := newDesk(t)
	watched := d.newSession("watched")
	d.keys("C-\\")
	other := d.newSession("other")
	d.keys("C-\\")
	_ = other

	// The viewport holds "other", so an alert on "watched" should notify.
	before := d.notifications()
	d.hook(watched, `{"hook_event_name":"Notification","message":"needs your permission to use Bash"}`)
	d.waitFor("a notification", func() bool { return d.notifications() > before })

	// Muted, the same alert says nothing.
	d.keys("M-M")
	d.waitFor("the mute notice", func() bool { return strings.Contains(d.sidebar(), "muted") })
	d.hook(watched, `{"hook_event_name":"SessionStart"}`)
	time.Sleep(600 * time.Millisecond)
	count := d.notifications()
	d.hook(watched, `{"hook_event_name":"Notification","message":"needs your permission to use Bash"}`)
	time.Sleep(1200 * time.Millisecond)
	if d.notifications() != count {
		t.Errorf("muted desk still notified: %d -> %d", count, d.notifications())
	}
	if !d.state().NotifyMuted {
		t.Error("the mute should be persisted")
	}
}

// Names come from transcripts, so a name carrying terminal escapes must be inert
// by the time it reaches the screen or tmux.
func TestHostileNameIsRenderedInert(t *testing.T) {
	d := newDesk(t)
	id := d.newSession("evil")
	d.keys("C-\\")

	// Rename it to something that would clear the screen and run a command if
	// either the sidebar or the tab bar took it literally.
	d.keys("Home")
	d.keys("M-r")
	d.keys("C-u")
	d.literal("\x1b[2Jzap #(touch " + filepath.Join(d.dir, "pwned") + ")")
	d.keys("Enter")

	d.waitFor("the rename to land", func() bool {
		for _, s := range d.state().Sessions {
			if s.ID == id {
				return strings.Contains(s.Name, "zap")
			}
		}
		return false
	})
	if _, err := os.Stat(filepath.Join(d.dir, "pwned")); err == nil {
		t.Fatal("a session name executed a command")
	}
	for _, s := range d.state().Sessions {
		if s.ID == id && strings.ContainsAny(s.Name, "\x1b\r\n") {
			t.Errorf("the stored name still holds control characters: %q", s.Name)
		}
	}
	if raw := d.rawTabs(); strings.Contains(raw, "#(") && !strings.Contains(raw, "##(") {
		t.Errorf("the tab bar holds an unescaped tmux format: %q", raw)
	}
}

// rawTabs is the tab bar with styling intact, for checking escaping.
func (d *desk) rawTabs() string {
	out, _ := d.tmux("show-options", "-t", "=agentboss:", "-v", "status-format[0]")
	return out
}

// f opens the folder a session runs in.
func TestOpenFolder(t *testing.T) {
	d := newDesk(t)
	d.newSession("withfolder")
	d.keys("C-\\")
	d.keys("Home")
	d.keys("M-f")
	d.waitFor("the opener to run", func() bool {
		data, err := os.ReadFile(d.openLog)
		return err == nil && strings.Contains(string(data), "withfolder")
	})
}

// Quitting leaves the agents running: that is the whole promise of the desk.
func TestQuitLeavesSessionsRunning(t *testing.T) {
	d := newDesk(t)
	id := d.newSession("survivor")
	d.keys("C-\\")
	d.waitFor("it to be open", func() bool { return len(d.liveSessions()) == 1 })

	d.keys("M-q", "y") // quit asks first
	d.waitFor("the manager session to go", func() bool {
		out, _ := d.tmux("has-session", "-t", "=agentboss:")
		return strings.Contains(out, "can't find") || strings.Contains(out, "no server")
	})
	live := d.liveSessions()
	if len(live) != 1 || live[0] != id {
		t.Errorf("the agent should still be running after quit, got %v", live)
	}
}

// The desk survives a restart with its sessions and grouping intact.
func TestDeskSurvivesAManagerRestart(t *testing.T) {
	d := newDesk(t)
	id := d.newSession("persistent")
	d.keys("C-\\")
	d.waitFor("it to be open", func() bool { return len(d.liveSessions()) == 1 })

	d.keys("M-q", "y") // quit asks first
	d.waitFor("the manager to exit", func() bool {
		out, _ := d.tmux("has-session", "-t", "=agentboss:")
		return strings.Contains(out, "can't find") || strings.Contains(out, "no server")
	})
	d.start()
	d.waitFor("the session to be listed again", func() bool {
		return strings.Contains(d.sidebar(), "persistent")
	})
	d.waitFor("its tab to come back", func() bool { return strings.Contains(d.tabs(), "persistent") })
	if got := d.liveSessions(); len(got) != 1 || got[0] != id {
		t.Errorf("the agent should have kept running across the restart, got %v", got)
	}
}

// Sanity: the manager holds a lock so two of them never fight over state.json.
func TestSecondManagerRefusesToStart(t *testing.T) {
	d := newDesk(t)
	cmd := exec.Command(d.bin, "__ui")
	cmd.Env = append(os.Environ(), "AGENTBOSS_HOME="+d.home, "TMUX_TMPDIR="+d.socket,
		"TERM=xterm-256color")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a second manager started; output: %s", out)
	}
	if !strings.Contains(string(out), "already running") {
		t.Errorf("expected a clear refusal, got: %s", out)
	}
}

// tabCount is a small helper used by the ordering test.
func tabCount(tabs string) int {
	return strings.Count(tabs, "·") + strings.Count(tabs, "⠴") + strings.Count(tabs, "◆") +
		strings.Count(tabs, "●") + strings.Count(tabs, "○")
}

// Tabs follow the sidebar's order, which is what makes the digit keys and the
// tab bar agree.
func TestTabOrderFollowsTheSidebar(t *testing.T) {
	d := newDesk(t)
	d.newSession("aaa")
	d.keys("C-\\")
	d.newSession("bbb")
	d.keys("C-\\")
	d.waitFor("two tabs", func() bool { return tabCount(d.tabs()) >= 2 })

	tabs := d.tabs()
	if strings.Index(tabs, "aaa") > strings.Index(tabs, "bbb") {
		t.Errorf("tab order should match the sidebar: %q", tabs)
	}
	// Move the second session up; the tabs must follow.
	d.keys("End")
	d.keys("M-K")
	d.waitFor("the reorder to show up in the tabs", func() bool {
		tabs := d.tabs()
		return strings.Index(tabs, "bbb") < strings.Index(tabs, "aaa")
	})
	if n, _ := strconv.Atoi("2"); tabCount(d.tabs()) < n {
		t.Errorf("expected both tabs to survive the reorder: %q", d.tabs())
	}
}

// Cycling sessions has to work while an agent holds the keyboard: the sidebar's
// [ and ] never reach tmux from inside a pane, which is the whole point of the
// root-table bindings.
func TestCycleKeysWorkFromInsideAnAgent(t *testing.T) {
	d := newDesk(t)
	d.newSession("one")
	d.keys("C-\\")
	d.newSession("two")
	d.waitFor("both to be open", func() bool { return len(d.liveSessions()) == 2 })

	// Focus stays in the viewport — where an agent, not the sidebar, has the keys.
	viewport := ""
	out, _ := d.tmux("list-panes", "-t", "agentboss:", "-F", "#{pane_id} #{@agentboss_role}")
	for _, l := range strings.Split(out, "\n") {
		if id, role, ok := strings.Cut(strings.TrimSpace(l), " "); ok && role == "viewport" {
			viewport = id
		}
	}
	if viewport == "" {
		t.Fatal("no viewport pane")
	}
	if _, err := d.tmux("select-pane", "-t", viewport); err != nil {
		t.Fatal(err)
	}

	// A client must exist for a key binding to fire, so attach one.
	if _, err := d.tmux("new-session", "-d", "-s", "holder", "-x", "160", "-y", "40",
		"TMUX= tmux attach-session -t agentboss"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)

	shown := func() string {
		out, _ := d.tmux("list-clients", "-F", "#{client_session}")
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, "adk_") {
				return strings.TrimPrefix(strings.TrimSpace(l), "adk_")
			}
		}
		return ""
	}
	before := shown()
	if before == "" {
		t.Fatal("the viewport is not showing a session")
	}

	// alt+] arrives as ESC ] — injected as raw bytes, exactly as a terminal sends
	// them, because the encoding is the whole question: ESC [ also begins every
	// control sequence.
	press := func(hex ...string) {
		args := append([]string{"send-keys", "-t", "holder", "-H"}, hex...)
		if _, err := d.tmux(args...); err != nil {
			t.Fatal(err)
		}
		time.Sleep(900 * time.Millisecond)
	}
	press("1b", "5d") // alt+]
	after := shown()
	if after == before {
		t.Fatalf("alt+] did not move off %s while the agent had focus", before)
	}
	press("1b", "5b") // alt+[
	if back := shown(); back != before {
		t.Errorf("alt+[ should have returned to %s, got %s", before, back)
	}
	// The csi-u form the desk asks terminals for must work too.
	press("1b", "5b", "39", "33", "3b", "33", "75") // ESC[93;3u = alt+]
	if csiu := shown(); csiu == before {
		t.Errorf("csi-u alt+] did not switch session (still %s)", before)
	}
	press("1b", "5b", "39", "31", "3b", "33", "75") // ESC[91;3u = alt+[
	if back := shown(); back != before {
		t.Errorf("csi-u alt+[ should have returned to %s, got %s", before, back)
	}
	// And the keyboard must still belong to the agent, not the sidebar.
	role, _ := d.tmux("display-message", "-p", "-t", "agentboss:", "#{@agentboss_role}")
	if strings.TrimSpace(role) != "viewport" {
		t.Errorf("focus moved out of the agent pane: role=%q", strings.TrimSpace(role))
	}

	// The digit keys work the same way: alt+1 (ESC 1) shows the first open
	// session from inside an agent, keyboard staying where it is.
	press("1b", "31") // alt+1
	first := d.state().Sessions[0].ID
	if now := shown(); now != first {
		t.Errorf("alt+1 showed %s, want the first open session %s", now, first)
	}
	press("1b", "32") // alt+2
	if now := shown(); now == first {
		t.Errorf("alt+2 did not move off the first session")
	}
	before = shown() // the escape-sequence probes below assert no movement

	// Binding the CSI prefix must not swallow the sequences that share it. Mouse
	// reporting is covered by the click and drag tests, which all run with these
	// bindings in place; here it is the keys that cannot reach the sidebar.
	for _, seq := range [][]string{
		{"1b", "5b", "41"},       // up arrow
		{"1b", "4f", "50"},       // F1
		{"1b", "5b", "31", "7e"}, // home
	} {
		press(seq...)
		if now := shown(); now != before {
			t.Errorf("%v was mistaken for a cycle key: session moved %s → %s", seq, before, now)
			break
		}
	}
}

// W creates the session in a fresh git worktree, so several agents can work
// on one repo without touching each other's files.
func TestWorktreeSession(t *testing.T) {
	d := newDesk(t)
	repo := filepath.Join(d.dir, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=e2e@test", "-c", "user.name=e2e", "commit", "--allow-empty", "-m", "root"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	d.keys("M-W", "C-u")
	d.literal(repo)
	d.keys("Enter") // repo accepted → worktree name prompt
	d.literal("try-1")
	d.keys("Enter") // worktree created → agent picker
	d.keys("Enter") // Claude
	d.waitFor("the session to appear", func() bool { return len(d.state().Sessions) == 1 })

	got := d.state().Sessions[0]
	// git resolves /tmp to /private/tmp on macOS; compare resolved paths.
	want := filepath.Join(d.dir, "work", ".worktrees", "repo-try-1")
	wantR, _ := filepath.EvalSymlinks(want)
	gotR, _ := filepath.EvalSymlinks(got.Dir)
	if gotR != wantR || gotR == "" {
		t.Fatalf("session dir = %q, want the worktree %q", got.Dir, want)
	}
	want = got.Dir
	out, err := exec.Command("git", "-C", want, "branch", "--show-current").Output()
	if err != nil || strings.TrimSpace(string(out)) != "try-1" {
		t.Fatalf("worktree branch = %q (%v), want try-1", strings.TrimSpace(string(out)), err)
	}
	// And the main checkout stayed on its own branch.
	out, _ = exec.Command("git", "-C", repo, "branch", "--show-current").Output()
	if strings.TrimSpace(string(out)) != "main" {
		t.Errorf("the repo's own checkout moved to %q", strings.TrimSpace(string(out)))
	}
}

// Action chords work from inside an agent: the whole keymap is desk-wide, not
// per-pane. alt+z pressed while the agent holds the keyboard must close the
// ACTIVE session's tab — confirm popup included, with focus pulled to the
// sidebar so the question can be answered.
func TestActionChordsWorkFromInsideAnAgent(t *testing.T) {
	d := newDesk(t)
	keep := d.newSession("keep")
	d.keys("C-\\")
	closeMe := d.newSession("close-me")
	d.waitFor("both to be open", func() bool { return len(d.liveSessions()) == 2 })

	// Put the keyboard in the agent, with a real client attached so root-table
	// bindings can fire.
	out, _ := d.tmux("list-panes", "-t", "agentboss:", "-F", "#{pane_id} #{@agentboss_role}")
	viewport := ""
	for _, l := range strings.Split(out, "\n") {
		if id, role, ok := strings.Cut(strings.TrimSpace(l), " "); ok && role == "viewport" {
			viewport = id
		}
	}
	if _, err := d.tmux("select-pane", "-t", viewport); err != nil {
		t.Fatal(err)
	}
	if _, err := d.tmux("new-session", "-d", "-s", "holder", "-x", "160", "-y", "40",
		"TMUX= tmux attach-session -t agentboss"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)

	// alt+z as raw bytes into the holder — exactly what a terminal sends.
	if _, err := d.tmux("send-keys", "-t", "holder", "-H", "1b", "7a"); err != nil {
		t.Fatal(err)
	}
	d.waitFor("the confirm popup to appear", func() bool {
		return strings.Contains(d.sidebar(), "close \"close-me\"'s tab?")
	})
	// Focus must have come to the sidebar, or y would go to the agent.
	out, _ = d.tmux("list-panes", "-t", "agentboss:", "-F", "#{pane_active} #{@agentboss_role}")
	if !strings.Contains(out, "1 sidebar") {
		t.Fatalf("focus did not move to the sidebar: %q", out)
	}
	d.keys("y")
	d.waitFor("the active session's tab to close", func() bool {
		for _, s := range d.liveSessions() {
			if s == closeMe {
				return false
			}
		}
		return true
	})
	if live := d.liveSessions(); len(live) != 1 || live[0] != keep {
		t.Errorf("wrong session closed: still live %v", live)
	}
}
