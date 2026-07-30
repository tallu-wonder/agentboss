// Package codexnotify installs agentdeck into Codex's `notify` hook without
// taking it over: whatever program was there keeps receiving every event.
//
// Codex's notify is a single program (unlike Claude Code's hook lists), so
// the only way to observe events without displacing an existing integration
// is to chain — agentdeck becomes the notify program, records what it
// replaced, and forwards each invocation verbatim.
package codexnotify

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/tallu-wonder/agentdeck/internal/paths"
)

// Chain is the displaced notify program, saved so we can forward to it.
type Chain struct {
	Program []string `json:"program"` // argv of the program we replaced
	SavedAt string   `json:"saved_at"`
}

// notifyLine matches a top-level `notify = [...]` assignment in config.toml.
// Codex's config is TOML; we deliberately rewrite only this one line rather
// than round-tripping the whole file through a TOML library, so comments,
// ordering, and every unrelated key survive byte-for-byte.
//
// The value is matched greedily to the end of the line: another tool chaining
// the same slot embeds a JSON argv containing ']' inside the array, and a lazy
// match would stop at that bracket and fail to recognize the line as a notify
// assignment at all. Missing it is unrecoverable rather than untidy — a second
// notify key is a duplicate TOML key, and Codex then refuses to start.
var notifyLine = regexp.MustCompile(`(?m)^[ \t]*notify[ \t]*=[ \t]*\[.*\][ \t]*$`)

// LoadChain returns the recorded displaced program, if any.
func LoadChain() Chain {
	var c Chain
	data, err := os.ReadFile(paths.NotifyChainFile())
	if err != nil {
		return c
	}
	_ = json.Unmarshal(data, &c)
	return c
}

// Installed reports whether agentdeck already receives Codex events: either
// notify points at it, or another chaining tool forwards to it (its argv shows
// up inside that tool's own notify line).
func Installed(binPath string) bool {
	for _, m := range notifyLines(paths.CodexConfigFile()) {
		if mentions(m, binPath) {
			return true
		}
	}
	return false
}

// mentions reports whether a notify line refers to path. A tool that chains to
// us embeds our argv as an escaped JSON string ("\/Users\/me\/..."), so the
// comparison ignores backslashes rather than matching the literal text.
func mentions(line, path string) bool {
	return strings.Contains(unescape(line), unescape(path))
}

func unescape(s string) string { return strings.ReplaceAll(s, `\`, "") }

// notifyLines returns every top-level notify assignment in the file. More
// than one is invalid TOML — Codex refuses to start — so callers must never
// add a line without checking.
func notifyLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range notifyLine.FindAll(data, -1) {
		out = append(out, string(m))
	}
	return out
}

// Install points Codex's notify at `binPath codex-notify`, preserving any
// existing program in the chain file. It backs up config.toml first and
// returns a human-readable description of what it did.
func Install(binPath string, force bool) (string, error) {
	cfgPath := paths.CodexConfigFile()
	data, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	want := fmt.Sprintf("notify = [%q, %q]", binPath, "codex-notify")

	lines := notifyLines(cfgPath)
	if len(lines) > 1 {
		return "", fmt.Errorf("%s has %d notify lines, which Codex rejects as a "+
			"duplicate key — remove all but one, then retry", cfgPath, len(lines))
	}
	for _, l := range lines {
		if mentions(l, binPath) {
			return "already installed", nil
		}
	}
	// Codex allows exactly one notify program, so installing means displacing
	// whatever is there. Another tool that manages the same slot may put
	// itself back (and some chain to us on their own), which is how a second
	// notify key — and an unstartable Codex — happens. Never do that silently.
	if len(lines) == 1 && !force {
		return "", fmt.Errorf("the Codex notify slot is already used by another program:\n  %s\n"+
			"agentdeck reads Codex status from its transcript instead, so nothing is broken.\n"+
			"To chain agentdeck in front of it anyway: agentdeck install-codex-notify --force",
			strings.TrimSpace(lines[0]))
	}

	existing := notifyLine.Find(data)

	// Record what we're displacing so codex-notify can forward to it.
	chain := Chain{SavedAt: time.Now().Format(time.RFC3339)}
	if existing != nil {
		if argv, ok := parseNotifyArray(string(existing)); ok && len(argv) > 0 {
			chain.Program = argv
		}
	}
	if err := writeJSON(paths.NotifyChainFile(), chain); err != nil {
		return "", err
	}

	var out []byte
	switch {
	case existing != nil:
		out = notifyLine.ReplaceAll(data, []byte(want))
	case len(data) == 0:
		out = []byte(want + "\n")
	default:
		// Prepend so the assignment lands before any [table] header — a
		// bare key after a table header would belong to that table.
		out = append([]byte(want+"\n"), data...)
	}

	if existing != nil {
		backup := cfgPath + ".agentdeck-backup"
		mode := os.FileMode(0o600)
		if fi, err := os.Stat(cfgPath); err == nil {
			mode = fi.Mode().Perm()
		}
		if err := os.WriteFile(backup, data, mode); err != nil {
			return "", err
		}
	}
	if err := writeAtomic(cfgPath, out); err != nil {
		return "", err
	}
	if len(chain.Program) > 0 {
		return "installed, forwarding to " + chain.Program[0], nil
	}
	return "installed", nil
}

// Uninstall restores the displaced notify program (or removes the line).
func Uninstall() error {
	cfgPath := paths.CodexConfigFile()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	chain := LoadChain()
	var repl string
	if len(chain.Program) > 0 {
		parts := make([]string, len(chain.Program))
		for i, a := range chain.Program {
			parts[i] = fmt.Sprintf("%q", a)
		}
		repl = "notify = [" + strings.Join(parts, ", ") + "]"
	}
	out := notifyLine.ReplaceAll(data, []byte(repl))
	if repl == "" {
		// Drop the now-empty line entirely.
		out = []byte(strings.ReplaceAll(string(out), "\n\n\n", "\n\n"))
	}
	if err := writeAtomic(cfgPath, out); err != nil {
		return err
	}
	os.Remove(paths.NotifyChainFile())
	return nil
}

// Forward re-invokes the displaced program with its own arguments plus the
// ones Codex passed us, so the existing integration sees every event exactly
// as before. Failures are ignored: a broken chain must never break Codex.
func Forward(args []string) {
	chain := LoadChain()
	if len(chain.Program) == 0 {
		return
	}
	argv := append([]string{}, chain.Program[1:]...)
	argv = append(argv, args...)
	cmd := exec.Command(chain.Program[0], argv...)
	cmd.Stdout, cmd.Stderr = nil, nil
	_ = cmd.Run()
}

// Event is the subset of Codex's notify payload agentdeck reads.
type Event struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread-id"`
	TurnID   string `json:"turn-id"`
	CWD      string `json:"cwd"`
}

// ParseEvent finds and decodes the JSON payload among the notify arguments.
// Codex appends it as the last argument, but we scan from the end so extra
// fixed arguments never confuse us.
func ParseEvent(args []string) (Event, bool) {
	for i := len(args) - 1; i >= 0; i-- {
		a := strings.TrimSpace(args[i])
		if !strings.HasPrefix(a, "{") {
			continue
		}
		var e Event
		if json.Unmarshal([]byte(a), &e) == nil && e.Type != "" {
			return e, true
		}
	}
	return Event{}, false
}

// parseNotifyArray pulls the argv out of a `notify = ["a", "b"]` line.
func parseNotifyArray(line string) ([]string, bool) {
	open := strings.Index(line, "[")
	close := strings.LastIndex(line, "]")
	if open < 0 || close < open {
		return nil, false
	}
	var argv []string
	if json.Unmarshal([]byte(line[open:close+1]), &argv) != nil {
		return nil, false
	}
	return argv, true
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(pathDir(path), ".adk-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	_ = os.Chmod(name, mode)
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func pathDir(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "."
}
