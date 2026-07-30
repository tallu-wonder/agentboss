// Package status is the tiny runtime channel between Claude Code hooks and
// the manager UI: one JSON file per session in ~/.agentdeck/status. Hooks
// write it (from short-lived `agentdeck hook` processes), the manager polls
// it. Kept out of state.json so the two writers never conflict.
package status

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tallu-wonder/agentdeck/internal/sanitize"
)

// Kind is a session's live status.
type Kind string

const (
	Working   Kind = "working"   // Claude is processing / running tools
	NeedsYou  Kind = "needs_you" // permission prompt or question is blocking
	Attention Kind = "attention" // finished a turn since you last looked
	Idle      Kind = "idle"      // running, nothing new for you
	Dormant   Kind = "dormant"   // no live tmux session (derived, not stored)
)

// Runtime is what a hook knows about a session at a moment in time.
type Runtime struct {
	Status          Kind      `json:"status"`
	ClaudeSessionID string    `json:"claude_session_id,omitempty"`
	Message         string    `json:"message,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// idPattern is the shape of the ids agentdeck generates. A session id becomes a
// filename here, so an id carrying "/" or ".." would place the file outside the
// status directory; reject rather than sanitize, since a malformed id means a
// caller bug or a tampered state file.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// file returns the status path for a session, or "" if the id is not one we
// could have generated.
func file(dir, sessionID string) string {
	if !idPattern.MatchString(sessionID) {
		return ""
	}
	return filepath.Join(dir, sessionID+".json")
}

// Read returns the runtime status for one session (zero value if absent).
func Read(dir, sessionID string) Runtime {
	var r Runtime
	path := file(dir, sessionID)
	if path == "" {
		return r
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return r
	}
	_ = json.Unmarshal(data, &r)
	return r
}

// ReadAll loads every status file in the directory, keyed by session ID.
func ReadAll(dir string) map[string]Runtime {
	out := map[string]Runtime{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		out[id] = Read(dir, id)
	}
	return out
}

// Write persists a session's runtime status atomically.
func Write(dir, sessionID string, r Runtime) error {
	dest := file(dir, sessionID)
	if dest == "" {
		return fmt.Errorf("refusing to write status for invalid session id %q", sessionID)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	r.UpdatedAt = time.Now()
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".st-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, dest)
}

// Remove deletes a session's status file (used when a session is deleted).
func Remove(dir, sessionID string) {
	if path := file(dir, sessionID); path != "" {
		os.Remove(path)
	}
}

// HookEvent is the subset of the JSON Claude Code pipes to hook commands
// that agentdeck cares about.
type HookEvent struct {
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id"`
	CWD           string `json:"cwd"`
	Message       string `json:"message"`
}

// Apply maps a hook event onto the previous runtime status and returns the
// new one, plus whether anything should be written.
func Apply(prev Runtime, ev HookEvent) (Runtime, bool) {
	next := prev
	if ev.SessionID != "" {
		next.ClaudeSessionID = ev.SessionID
	}
	switch ev.HookEventName {
	case "SessionStart":
		next.Status = Idle
		next.Message = ""
	case "UserPromptSubmit", "PreToolUse", "PostToolUse":
		next.Status = Working
		next.Message = ""
	case "Notification":
		// Only a blocking request (permission prompt / question) is a red
		// "needs you". Claude also sends a notification when a session has
		// merely been idle a while — that's a soft "look at me".
		if strings.Contains(strings.ToLower(ev.Message), "permission") {
			next.Status = NeedsYou
		} else {
			next.Status = Attention
		}
		next.Message = sanitize.Line(ev.Message)
	case "Stop":
		next.Status = Attention
		next.Message = ""
	case "SessionEnd":
		next.Status = Idle
		next.Message = ""
	default:
		return prev, false
	}
	return next, true
}
