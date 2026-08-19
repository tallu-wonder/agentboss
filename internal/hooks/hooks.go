// Package hooks installs agentboss's status hooks into Claude Code's
// settings.json. The hooks call `agentboss hook`, a fast subcommand that
// records each session's live status (working / needs you / finished).
//
// The settings file is edited conservatively: parsed as a generic map so
// every unrelated key survives byte-for-byte semantically, and our entries
// are only appended if no "agentboss hook" command is already registered for
// that event.
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Events agentboss listens to. Together they yield the full status lifecycle:
// idle -> working -> (needs_you) -> attention -> idle.
var Events = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PreToolUse",
	"PostToolUse",
	"Notification",
	"Stop",
	"SessionEnd",
}

// ourCommand matches ONLY a command agentboss itself would have written: an
// optionally quoted path whose final element is the agentboss binary, followed
// by the bare `hook` subcommand.
//
// The match has to be this strict because a match is rewritten in place. A
// loose "mentions agentboss and hook" test would also match a user's own
// wrapper — `notify.sh && agentboss hook`, say — and replacing that would
// destroy configuration agentboss did not write.
// agentdeck is this project's pre-rename name: recognizing it means an upgrade
// rewrites the old hooks instead of stacking a second set beside them.
var ourCommand = regexp.MustCompile(`^'?(?:[^\s'"|;&]*/)?agent(?:boss|deck)'? hook$`)

// isOurs recognizes a previously installed agentboss hook command, with or
// without shell quoting around the binary path.
func isOurs(cmd string) bool {
	return ourCommand.MatchString(strings.TrimSpace(cmd))
}

// shellQuote wraps s in single quotes for safe embedding in a shell command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Install ensures every agentboss hook is present in settingsPath. binPath is
// the absolute path to the agentboss binary. Returns true if the file was
// modified.
func Install(settingsPath, binPath string) (bool, error) {
	root := map[string]any{}
	data, err := os.ReadFile(settingsPath)
	switch {
	case err == nil:
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &root); err != nil {
				return false, fmt.Errorf("cannot parse %s (won't touch it): %w", settingsPath, err)
			}
		}
	case errors.Is(err, os.ErrNotExist):
		// fresh file
	default:
		return false, err
	}

	// Refuse to rewrite structures we don't understand — never destroy a
	// user's hand-written hook configuration.
	if v, ok := root["hooks"]; ok {
		if _, isMap := v.(map[string]any); !isMap {
			return false, fmt.Errorf("%s has an unexpected \"hooks\" type; won't touch it", settingsPath)
		}
	}
	hooksAny, _ := root["hooks"].(map[string]any)
	if hooksAny == nil {
		hooksAny = map[string]any{}
	}

	want := shellQuote(binPath) + " hook"
	changed := false
	for _, ev := range Events {
		if v, ok := hooksAny[ev]; ok {
			if _, isList := v.([]any); !isList {
				return false, fmt.Errorf("%s: hooks.%s has an unexpected type; won't touch it", settingsPath, ev)
			}
		}
		entries, _ := hooksAny[ev].([]any)
		switch upsertOurs(entries, want) {
		case upsertCurrent:
			continue
		case upsertHealed:
			changed = true // stale binary path rewritten in place
			continue
		}
		entry := map[string]any{
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": want,
					"timeout": 5,
				},
			},
		}
		hooksAny[ev] = append(entries, any(entry))
		changed = true
	}
	if !changed {
		return false, nil
	}
	root["hooks"] = hooksAny

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return false, err
	}
	// Keep one copy of the file as it was before agentboss first touched it.
	// Editing someone's Claude Code settings deserves an undo, and a single
	// backup (never overwritten) is the version they'd want back.
	if len(data) > 0 {
		backup := settingsPath + ".agentboss-backup"
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			_ = os.WriteFile(backup, data, 0o600)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(settingsPath), ".settings-*.json")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return false, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return false, err
	}
	// Keep the file's original permissions (CreateTemp defaults to 0600).
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(settingsPath); err == nil {
		mode = fi.Mode().Perm()
	}
	_ = os.Chmod(tmpName, mode)
	if err := os.Rename(tmpName, settingsPath); err != nil {
		os.Remove(tmpName)
		return false, err
	}
	return true, nil
}

type upsertResult int

const (
	upsertMissing upsertResult = iota // no agentboss entry: append one
	upsertCurrent                     // entry present and up to date
	upsertHealed                      // entry present, stale path rewritten
)

// upsertOurs finds an existing agentboss hook entry; when its command is
// stale (binary moved/reinstalled) it is fixed in place, so status tracking
// self-heals instead of silently pointing at a dead path forever.
func upsertOurs(entries []any, want string) upsertResult {
	for _, e := range entries {
		m, _ := e.(map[string]any)
		inner, _ := m["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			cmd, _ := hm["command"].(string)
			if !isOurs(cmd) {
				continue
			}
			if cmd == want {
				return upsertCurrent
			}
			hm["command"] = want
			return upsertHealed
		}
	}
	return upsertMissing
}
