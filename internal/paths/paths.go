// Package paths centralizes every filesystem location agentdeck uses, so the
// whole app can be pointed at a sandbox via AGENTDECK_HOME (used by tests).
package paths

import (
	"os"
	"path/filepath"
)

// Home returns the agentdeck data directory (~/.agentdeck by default,
// overridable with AGENTDECK_HOME).
func Home() string {
	if h := os.Getenv("AGENTDECK_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".agentdeck"
	}
	return filepath.Join(home, ".agentdeck")
}

// StateFile is the persistent desk layout (groups, sessions, order).
func StateFile() string { return filepath.Join(Home(), "state.json") }

// StatusDir holds one small JSON file per session, written by Claude Code
// hooks and read by the manager. Kept separate from the state file so hook
// writes never race the manager's saves.
func StatusDir() string { return filepath.Join(Home(), "status") }

// CmdDir holds one-shot command files from helper subprocesses (tab drag
// drops) for the manager to apply — only the manager writes state.json.
func CmdDir() string { return filepath.Join(Home(), "cmd") }

// CodexSessionsDir is Codex's rollout-transcript store
// (~/.codex/sessions/<year>/<month>/<day>/rollout-*.jsonl).
// Overridable with AGENTDECK_CODEX_SESSIONS (used by tests).
func CodexSessionsDir() string {
	if d := os.Getenv("AGENTDECK_CODEX_SESSIONS"); d != "" {
		return d
	}
	return filepath.Join(codexHome(), "sessions")
}

// CodexSessionIndex is Codex's append-only name index
// (~/.codex/session_index.jsonl): one {id, thread_name, updated_at} record per
// rename, last write wins. It is what `codex resume` shows in its picker.
func CodexSessionIndex() string {
	return filepath.Join(codexHome(), "session_index.jsonl")
}

// CodexConfigFile is Codex's config.toml, where the notify hook lives.
func CodexConfigFile() string { return filepath.Join(codexHome(), "config.toml") }

func codexHome() string {
	if d := os.Getenv("AGENTDECK_CODEX_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

// NotifyChainFile records the Codex notify program agentdeck displaced, so
// `agentdeck codex-notify` can forward every event to it unchanged.
func NotifyChainFile() string { return filepath.Join(Home(), "codex-notify-chain.json") }

// CrashFile is where the manager records a panic before restarting itself.
func CrashFile() string { return filepath.Join(Home(), "crash.log") }

// HostFile records which terminal application the manager is running in, so
// that clicking a notification can raise that window before switching sessions.
func HostFile() string { return filepath.Join(Home(), "host.json") }

func ClaudeSettingsFile() string {
	if p := os.Getenv("AGENTDECK_CLAUDE_SETTINGS"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// EnsureDirs creates the agentdeck directories if missing.
func EnsureDirs() error {
	// A desk created by an older version (or by hand) may be world-readable.
	// Tighten it: the folders you work in, your session names and their live
	// status are nobody else's business on a shared machine.
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(StatusDir(), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(CmdDir(), 0o700); err != nil {
		return err
	}
	for _, d := range []string{Home(), StatusDir(), CmdDir()} {
		if fi, err := os.Stat(d); err == nil && fi.Mode().Perm() != 0o700 {
			_ = os.Chmod(d, 0o700) // best effort: never block startup on it
		}
	}
	return nil
}
