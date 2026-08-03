// Package tmuxctl wraps the tmux CLI. tmux is agentdeck's process supervisor:
// every Claude session lives in its own tmux session, so sessions survive
// manager restarts, terminal crashes, and closed windows.
package tmuxctl

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ManagerSession is the tmux session the manager UI lives in.
const ManagerSession = "agentdeck"

// Info describes one live tmux session.
type Info struct {
	Name     string
	Attached bool
}

func run(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Available reports whether tmux is installed.
func Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// InsideTmux reports whether this process runs inside a tmux client.
func InsideTmux() bool { return os.Getenv("TMUX") != "" }

// ServerAlive reports whether the tmux server referenced by the current
// environment actually answers ($TMUX can be stale after a server death).
func ServerAlive() bool {
	return exec.Command("tmux", "list-sessions").Run() == nil
}

// ListSessions returns all live tmux sessions keyed by name. A missing/empty
// server is not an error — it returns an empty map.
func ListSessions() map[string]Info {
	out := map[string]Info{}
	raw, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}\t#{session_attached}").Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		out[parts[0]] = Info{Name: parts[0], Attached: parts[1] != "0"}
	}
	return out
}

// Has reports whether a session with this exact name exists.
func Has(name string) bool {
	err := exec.Command("tmux", "has-session", "-t", "="+name).Run()
	return err == nil
}

// NewSession creates a detached session running command in dir, with extra
// environment variables set for every process spawned inside it.
func NewSession(name, dir string, env map[string]string, command string) error {
	args := []string{"new-session", "-d", "-s", name, "-c", dir, "-x", "220", "-y", "50"}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	if command != "" {
		args = append(args, command)
	}
	_, err := run(args...)
	return err
}

// KillSession terminates a session (and the agent process inside it).
func KillSession(name string) error {
	_, err := run("kill-session", "-t", "="+name)
	return err
}

// paneTarget builds a pane-level target: bare "=name" only resolves for
// session-level commands, pane/window commands need the trailing colon.
func paneTarget(name string) string { return "=" + name + ":" }

// CapturePane returns the current visible contents of a session's active
// pane with ANSI colors preserved.
func CapturePane(name string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-e", "-N", "-t", paneTarget(name)).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// PaneSize returns the active pane's width and height.
func PaneSize(name string) (w, h int, err error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", paneTarget(name), "#{pane_width} #{pane_height}").Output()
	if err != nil {
		return 0, 0, err
	}
	_, err = fmt.Sscanf(strings.TrimSpace(string(out)), "%d %d", &w, &h)
	return w, h, err
}

// ResizeWindow forces a detached session's window to the given size so the
// preview is rendered at exactly the size we display it. (tmux flips the
// window to manual sizing; SwitchClient undoes that before attaching.)
func ResizeWindow(name string, w, h int) error {
	_, err := run("resize-window", "-t", paneTarget(name), "-x", fmt.Sprint(w), "-y", fmt.Sprint(h))
	return err
}

// SwitchClient moves the user's tmux client to the target session, restoring
// automatic window sizing first so the app re-renders at full client size.
func SwitchClient(name string) error {
	// Undo any manual sizing left over from preview rendering; ignore errors.
	exec.Command("tmux", "set-option", "-w", "-t", paneTarget(name), "-u", "window-size").Run()
	_, err := run("switch-client", "-t", "="+name)
	return err
}

// BindReturnKey installs a prefix-less global binding: inside agentdeck it
// toggles focus between the sidebar and the session viewport; from any other
// tmux session it jumps to agentdeck. Idempotent (rebinding overwrites).
func BindReturnKey(key string) error {
	_, err := run("bind-key", "-n", key,
		"if", "-F", "#{==:#{session_name},"+ManagerSession+"}",
		"select-pane -l", "switch-client -t ="+ManagerSession)
	return err
}

// BindCycleKeys makes prev/next work from anywhere on the desk, including while
// an agent has the keyboard.
//
// The sidebar's [ and ] only reach the sidebar: once focus is in the viewport the
// keystrokes belong to the agent. A root-table binding is intercepted by tmux
// before the pane sees it, which is the same reason the return key works from
// inside a session.
//
// Keys with a bare bracket or brace are deliberately not the default: alt+[
// arrives as ESC [, which is also how a control sequence starts, so tmux cannot
// tell the two apart.
func BindCycleKeys(binPath, prevKey, nextKey string) error {
	for _, b := range []struct{ key, dir string }{{prevKey, "prev"}, {nextKey, "next"}} {
		if b.key == "" {
			continue
		}
		_, err := run("bind-key", "-n", b.key,
			"if", "-F", "#{==:#{session_name},"+ManagerSession+"}",
			"run-shell \""+binPath+" _tab "+b.dir+"\"",
			"send-keys "+b.key)
		if err != nil {
			return err
		}
	}
	return nil
}

// PaneInfo describes one pane of the manager window.
type PaneInfo struct {
	ID   string // "%3"
	Role string // @agentdeck_role: "sidebar" | "viewport"
	TTY  string
	Dead bool
	W, H int
}

// Panes lists the panes of the manager window with their agentdeck roles.
func Panes() ([]PaneInfo, error) {
	out, err := exec.Command("tmux", "list-panes", "-t", "="+ManagerSession+":",
		"-F", "#{pane_id}\t#{@agentdeck_role}\t#{pane_tty}\t#{pane_dead}\t#{pane_width}\t#{pane_height}").Output()
	if err != nil {
		return nil, err
	}
	var panes []PaneInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 6 {
			continue
		}
		p := PaneInfo{ID: f[0], Role: f[1], TTY: f[2], Dead: f[3] == "1"}
		fmt.Sscanf(f[4], "%d", &p.W)
		fmt.Sscanf(f[5], "%d", &p.H)
		panes = append(panes, p)
	}
	return panes, nil
}

// SetPaneRole tags a pane so it can be found again after restarts.
func SetPaneRole(paneID, role string) error {
	return SetPaneOption(paneID, "@agentdeck_role", role)
}

// SetPaneOption sets a pane-scoped option.
func SetPaneOption(paneID, opt, val string) error {
	_, err := run("set-option", "-p", "-t", paneID, opt, val)
	return err
}

// SplitViewport creates the viewport pane to the right of the sidebar pane,
// running command, and returns its pane id.
func SplitViewport(sidebarPane, command string) (string, error) {
	out, err := exec.Command("tmux", "split-window", "-h", "-d", "-t", sidebarPane,
		"-P", "-F", "#{pane_id}", command).Output()
	if err != nil {
		return "", fmt.Errorf("split-window: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ResizePane sets a pane's width.
func ResizePane(paneID string, w int) error {
	_, err := run("resize-pane", "-t", paneID, "-x", fmt.Sprint(w))
	return err
}

// SelectPane focuses a pane.
func SelectPane(paneID string) error {
	_, err := run("select-pane", "-t", paneID)
	return err
}

// RespawnPane kills whatever runs in the pane and starts command instead.
func RespawnPane(paneID, command string) error {
	_, err := run("respawn-pane", "-k", "-t", paneID, command)
	return err
}

// ClientSessions maps client tty -> attached session name.
func ClientSessions() map[string]string {
	out := map[string]string{}
	raw, err := exec.Command("tmux", "list-clients", "-F", "#{client_tty}\t#{session_name}").Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.SplitN(line, "\t", 2)
		if len(f) == 2 {
			out[f[0]] = f[1]
		}
	}
	return out
}

// SendLine clears the target pane's input line and types text + Enter.
func SendLine(name, text string) error {
	t := paneTarget(name)
	if _, err := run("send-keys", "-t", t, "C-u"); err != nil {
		return err
	}
	if _, err := run("send-keys", "-t", t, "-l", "--", text); err != nil {
		return err
	}
	_, err := run("send-keys", "-t", t, "Enter")
	return err
}

// SwitchClientOn moves the client on the given tty to another session.
func SwitchClientOn(tty, session string) error {
	_, err := run("switch-client", "-c", tty, "-t", "="+session)
	return err
}

// SetStatusFormat pushes the manager-rendered tab bar into the agentdeck
// session's status line.
func SetStatusFormat(format string) error {
	_, err := run("set-option", "-t", paneTarget(ManagerSession), "status-format[0]", format)
	return err
}

// ConfigureServer applies the server-wide options and key binding agentdeck
// needs so shift+enter inserts a newline in an agent instead of submitting the
// prompt — through BOTH tmux hops (the outer session and the nested viewport
// client).
//
// extended-keys lets modified keys reach apps that ask for them, and the csi-u
// format is the encoding both agents understand (tmux defaults to the older
// xterm encoding, ESC[27;2;13~, which Codex ignores). That alone is not
// enough, though — see bindNewlineKey.
func ConfigureServer() {
	// Selecting text in a pane must reach the system clipboard. tmux defaults
	// set-clipboard to "external", which sends its own copies onward but
	// DISCARDS clipboard writes coming from applications — and inside the
	// viewport the inner tmux client is an application, so a copy made in a
	// pane died at the outer layer. "on" accepts and forwards it.
	//
	// It only ever mattered for Codex: Codex requests no mouse tracking, so
	// tmux handles the drag and does the copying; Claude Code grabs the mouse
	// itself and copies through pbcopy, never touching tmux.
	_, _ = run("set-option", "-s", "set-clipboard", "on")
	_, _ = run("set-option", "-s", "extended-keys", "on")
	_, _ = run("set-option", "-s", "extended-keys-format", "csi-u")
	_, _ = run("set-option", "-s", "-a", "terminal-features", "*:extkeys")
	bindNewlineKey()
}

// bindNewlineKey translates shift+enter into ESC CR before it reaches the pane.
//
// tmux forwards modified keys only to applications that request them with the
// classic modifyOtherKeys sequence. Claude Code does; Codex instead requests
// the kitty keyboard protocol (ESC[>7u) and turns modifyOtherKeys off outright
// (ESC[>4;0m) — a request tmux does not recognize. So tmux sends Codex NO
// encoding of shift+enter at all, whatever extended-keys-format says: Codex
// sees a bare carriage return and submits the prompt.
//
// Binding the chord sidesteps key negotiation entirely. ESC CR is accepted as
// "insert a newline" by both Claude Code and Codex (verified by driving each
// real CLI and watching its composer), and passes through the nested viewport
// client unchanged. Scoped to the agentdeck session so other tmux sessions
// keep tmux's default behavior.
func bindNewlineKey() {
	_, _ = run("bind-key", "-n", "S-Enter",
		"if", "-F", "#{==:#{session_name},"+ManagerSession+"}",
		"send-keys -H 1b 0d", "send-keys S-Enter")
}

// HostTermProgram returns $TERM_PROGRAM as it was OUTSIDE tmux — the terminal
// application the desk is actually running in.
//
// Inside a pane tmux overwrites TERM_PROGRAM with "tmux", so the obvious lookup
// reports the multiplexer instead of the terminal. tmux does keep the original
// in its global environment (seeded from the client that started the server),
// which is where the real value comes from.
func HostTermProgram() string {
	if v := os.Getenv("TERM_PROGRAM"); v != "" && v != "tmux" && v != "screen" {
		return v
	}
	out, err := run("show-environment", "-g", "TERM_PROGRAM")
	if err != nil {
		return ""
	}
	_, value, ok := strings.Cut(strings.TrimSpace(out), "=")
	if !ok {
		return ""
	}
	return value
}

// CapturePanePlain returns the visible pane text without escape sequences.
func CapturePanePlain(name string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", paneTarget(name)).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ConfigureManagerSession applies the outer session's look & behavior: the
// status line is the tab bar (top), mouse is on, and the prefix is moved off
// C-b so claude's readline keys pass through untouched.
func ConfigureManagerSession() {
	opts := [][2]string{
		{"status", "on"},
		{"status-position", "top"},
		{"status-interval", "0"},
		{"status-left", ""},
		{"status-right", ""},
		{"status-style", "bg=colour235,fg=colour250"},
		{"mouse", "on"},
		{"prefix", "C-q"},
		// Quitting agentdeck must return the user to their shell, not dump
		// them into a raw agent session.
		{"detach-on-destroy", "on"},
	}
	for _, kv := range opts {
		_ = SetSessionOption(ManagerSession, kv[0], kv[1])
	}
}

// ConfigureAgentSession applies per-session options to an agent session: no
// inner status bar (tabs + sidebar already carry the name), prefix off C-b,
// mouse on for scrollback.
func ConfigureAgentSession(name string) {
	for _, kv := range [][2]string{
		{"status", "off"},
		{"prefix", "C-q"},
		{"mouse", "on"},
	} {
		_ = SetSessionOption(name, kv[0], kv[1])
	}
}

// BindStatusClicks makes tab-bar clicks work: left-click on a
// #[range=user|X] segment opens that session, middle-click closes its tab
// (kills the process, keeps it on the desk). Clicks on other sessions'
// status lines keep tmux's default behavior.
func BindStatusClicks(binPath string) error {
	if _, err := run("bind-key", "-T", "root", "MouseDown1Status",
		"if", "-F", "#{==:#{session_name},"+ManagerSession+"}",
		"run-shell \""+binPath+" _tab '#{mouse_status_range}'\"",
		"select-window -t="); err != nil {
		return err
	}
	if _, err := run("bind-key", "-T", "root", "MouseDown2Status",
		"if", "-F", "#{==:#{session_name},"+ManagerSession+"}",
		"run-shell \""+binPath+" _tabclose '#{mouse_status_range}'\"", ""); err != nil {
		return err
	}
	// Dragging a tab: MouseDown1Status (above, via _tab) records the grabbed
	// tab and switches to it; releasing the drag over another tab reorders.
	if _, err := run("bind-key", "-T", "root", "MouseDragEnd1Status",
		"if", "-F", "#{==:#{session_name},"+ManagerSession+"}",
		"run-shell \""+binPath+" _tabdrop '#{mouse_status_range}'\"", ""); err != nil {
		return err
	}
	// Right-click a tab: context menu (open / close / close → old).
	_, err := run("bind-key", "-T", "root", "MouseDown3Status",
		"if", "-F", "#{==:#{session_name},"+ManagerSession+"}",
		"run-shell \""+binPath+" _tabmenu '#{mouse_status_range}' '#{mouse_x}'\"", "")
	return err
}

// SetSessionOption sets a session-scoped option. Note the "=name:" target
// form: set-option rejects a bare "=name" ("no such session") even though
// most other commands accept it.
func SetSessionOption(name, opt, val string) error {
	_, err := run("set-option", "-t", paneTarget(name), opt, val)
	return err
}

// DisplayMessage flashes a short message in the tmux status line of the
// client attached to the given session.
func DisplayMessage(msg string) {
	exec.Command("tmux", "display-message", msg).Run()
}
