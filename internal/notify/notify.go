// Package notify posts desktop notifications when a session wants attention,
// and makes clicking one jump straight to that session.
//
// The click action is the whole point: a notification that only says "something
// happened" still leaves you hunting through thirty sessions. So the preferred
// backend is terminal-notifier, whose -execute runs `agentboss focus <id>` when
// the notification is clicked. Where that isn't installed we still notify (via
// AppleScript on macOS, notify-send on Linux) but say so, rather than silently
// dropping the feature.
package notify

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// Kind is why a session is asking for you.
type Kind string

const (
	// NeedsYou is a blocking request: a permission prompt or a question.
	NeedsYou Kind = "needs-you"
	// Finished is output produced since you last looked.
	Finished Kind = "finished"
)

// Alert is one notification's worth of information about a session.
type Alert struct {
	SessionID string
	Name      string
	Folder    string // display form, e.g. "~/GitHub/agentboss"
	Agent     string // "claude" | "codex"
	Kind      Kind
}

// EnvMode selects which KINDS of alert notify: "" or "all" for every alert,
// "needsyou" for blocking requests only, "off" for none.
//
// Silencing alerts while you sit at the desk is a toggle in the UI rather than a
// mode here, because whether you are looking at the desk cannot be detected:
// terminals report focus per window, so another tab of the same terminal is
// indistinguishable from the desk itself.
const EnvMode = "AGENTBOSS_NOTIFY"

// safeID guards the one field that reaches a shell: terminal-notifier's
// -execute takes a command STRING, so a session id containing shell syntax
// would be executed. Our ids are generated ("s_1f3a…"), so anything else is
// refused and the notification simply loses its click action.
var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Mode returns the configured notification mode, normalized.
func Mode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvMode))) {
	case "off", "none", "0", "false":
		return "off"
	case "needsyou", "needs-you", "blocking":
		return "needsyou"
	default:
		// Anything unrecognized — including unset — notifies rather than
		// silently dropping alerts.
		return "all"
	}
}

// Wanted reports whether this kind of alert should notify.
func Wanted(k Kind) bool {
	switch Mode() {
	case "off":
		return false
	case "needsyou":
		return k == NeedsYou
	}
	return true
}

// TerminalApp maps $TERM_PROGRAM to the macOS application name, so a click on a
// notification can raise the terminal running the desk. Unrecognized terminals
// return "", which callers read as "nothing to raise".
func TerminalApp(termProgram string) string {
	switch strings.ToLower(strings.TrimSpace(termProgram)) {
	case "ghostty":
		return "Ghostty"
	case "apple_terminal":
		return "Terminal"
	case "iterm.app":
		return "iTerm2"
	case "wezterm":
		return "WezTerm"
	case "warpterminal", "warp":
		return "Warp"
	case "hyper":
		return "Hyper"
	case "tabby":
		return "Tabby"
	case "alacritty":
		return "Alacritty"
	case "kitty":
		return "kitty"
	case "vscode":
		return "Code"
	}
	return ""
}

// Backend describes how notifications will be delivered.
type Backend struct {
	Program   string // "" when nothing is available
	Clickable bool   // whether clicking jumps to the session
}

// Detect picks the best available backend.
func Detect() Backend {
	if p, err := exec.LookPath("terminal-notifier"); err == nil {
		return Backend{Program: p, Clickable: true}
	}
	if runtime.GOOS == "darwin" {
		if p, err := exec.LookPath("osascript"); err == nil {
			return Backend{Program: p}
		}
	}
	if p, err := exec.LookPath("notify-send"); err == nil {
		return Backend{Program: p}
	}
	return Backend{}
}

// Send posts one notification. selfBin is the agentboss binary that a click
// should re-invoke. It never blocks: the notifier is launched and reaped in the
// background, because this runs inside the manager's event loop.
func Send(selfBin string, a Alert) error {
	if !Wanted(a.Kind) {
		return nil
	}
	b := Detect()
	if b.Program == "" {
		return fmt.Errorf("no notification program found")
	}
	title, subtitle := a.Name, "needs your input"
	if a.Kind == Finished {
		subtitle = "finished"
	}
	body := a.Folder
	if a.Agent != "" {
		body = a.Agent + " · " + a.Folder
	}

	var argv []string
	switch {
	case strings.HasSuffix(b.Program, "terminal-notifier"):
		argv = []string{
			"-title", title,
			"-subtitle", subtitle,
			"-message", body,
			// One slot per session: a new alert replaces that session's old
			// one instead of stacking up while you're away.
			"-group", "agentboss:" + a.SessionID,
		}
		if a.Kind == NeedsYou {
			argv = append(argv, "-sound", "default")
		}
		if safeID.MatchString(a.SessionID) {
			argv = append(argv, "-execute", shellQuote(selfBin)+" focus "+a.SessionID)
		}
	case strings.HasSuffix(b.Program, "osascript"):
		argv = []string{"-e", fmt.Sprintf(
			"display notification %s with title %s subtitle %s",
			appleStr(body), appleStr(title), appleStr(subtitle))}
	default: // notify-send
		argv = []string{"-a", "agentboss", title + " — " + subtitle, body}
	}

	cmd := exec.Command(b.Program, argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// shellQuote wraps s for /bin/sh, which is what terminal-notifier's -execute
// string is handed to.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// appleStr renders a Go string as an AppleScript string literal. Session names
// are user- and agent-authored, so quotes and backslashes must not be able to
// end the literal and continue the script.
func appleStr(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	return `"` + s + `"`
}
