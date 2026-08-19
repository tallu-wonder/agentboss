// Package agents is the seam between the desk UI and the agent CLIs it
// hosts. Each supported CLI (Claude Code, Codex) implements Provider, so the
// UI never branches on agent kind: it asks the provider how to wake a
// session, where its transcript lives, and what that transcript says.
package agents

import (
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tallu-wonder/agentboss/internal/claudesessions"
	"github.com/tallu-wonder/agentboss/internal/codexsessions"
	"github.com/tallu-wonder/agentboss/internal/state"
	"github.com/tallu-wonder/agentboss/internal/status"
)

// Info is the agent-neutral view of a session's transcript.
type Info struct {
	Title         string
	TitleExplicit bool      // an explicit name record, not a derived summary
	Model         string    // raw model id ("claude-opus-4-8", "gpt-5.6-sol")
	Family        string    // display label ("opus", "gpt-5.6")
	ContextTokens int       // context size of the last turn
	ContextWindow int       // 0 = unknown, caller falls back to a guess
	TodosDone     int       // Claude only
	TodosTotal    int       // Claude only
	CurrentTodo   string    // Claude only
	Born          time.Time // when the conversation started
	// Phase is a transcript-derived lifecycle hint, used where the agent has
	// no hook: "working", "done", "aborted", or "" when unknown.
	Phase string
}

// Conversation is a past session discovered on disk, for the import picker.
type Conversation struct {
	Agent     string
	SessionID string
	Dir       string
	Title     string
	MTime     time.Time
}

// CostState carries incremental cost accounting between probes so a growing
// transcript is never rescanned from the start.
type CostState struct {
	Offset int64
	Total  float64
}

// Provider adapts one agent CLI to the desk.
type Provider interface {
	// Kind is the stable identifier stored in state.json.
	Kind() string
	// Label is the short display name shown in pickers and popups.
	Label() string
	// Binary is the command to launch, honoring the env override.
	Binary() string
	// Installed reports whether the CLI is present on this machine.
	Installed() bool
	// ResumeArgs is how to continue an existing conversation.
	ResumeArgs(sessionID string) []string
	// TranscriptPath locates a conversation's transcript, "" if not found.
	TranscriptPath(sessionID string) string
	// ScratchDir is where the agent keeps the session's own working files
	// (Claude Code's per-session scratchpad). "" when the agent has no such
	// directory, or hasn't created one yet.
	ScratchDir(sessionID string) string
	// Adopt finds the conversation an agent started in dir at/after
	// notBefore, for agents that don't announce their ID up front. Returns
	// "" when nothing matches.
	Adopt(dir string, notBefore time.Time) string
	// Probe reads a transcript.
	Probe(path string) Info
	// LiveName is the name the CLI itself is showing for a running session
	// ("" when the agent has no live registry).
	LiveName(sessionID string) string
	// Scan lists past conversations for the import picker.
	Scan(exclude map[string]bool, limit int) []Conversation
	// Rename records a new name where the agent can see it. Returning false
	// means the rename is local to the desk only.
	Rename(sessionID, name string) bool
	// Cost accumulates estimated spend. A zero Total means "not priced".
	Cost(path string, prev CostState) CostState
	// StatusFromPhase maps a transcript phase onto a desk status, for agents
	// whose status comes from the transcript rather than hooks.
	StatusFromPhase(phase string) status.Kind
}

// All returns every provider, Claude first.
func All() []Provider { return []Provider{claudeProvider{}, codexProvider{}} }

// Get returns the provider for a kind, defaulting to Claude.
func Get(kind string) Provider {
	if kind == state.AgentCodex {
		return codexProvider{}
	}
	return claudeProvider{}
}

func lookup(envVar, def string) string {
	if v := envOverride(envVar); v != "" {
		return v
	}
	if p, err := exec.LookPath(def); err == nil {
		return p
	}
	return def // resolved by the shell at wake time
}

// ---- Claude ------------------------------------------------------------

type claudeProvider struct{}

func (claudeProvider) Kind() string  { return state.AgentClaude }
func (claudeProvider) Label() string { return "claude" }
func (claudeProvider) Binary() string {
	return lookup("AGENTBOSS_CLAUDE_CMD", "claude")
}
func (c claudeProvider) Installed() bool { return installed(c.Binary()) }
func (claudeProvider) ResumeArgs(sessionID string) []string {
	return []string{"--resume", sessionID}
}
func (claudeProvider) TranscriptPath(sessionID string) string {
	return claudesessions.TranscriptPath(sessionID)
}
func (claudeProvider) ScratchDir(sessionID string) string {
	return claudesessions.ScratchDir(sessionID)
}

func (claudeProvider) Probe(path string) Info {
	title, ti := claudesessions.Probe(path)
	return Info{
		Title:         title,
		TitleExplicit: ti.Name != "",
		Model:         ti.Model,
		Family:        claudesessions.Family(ti.Model),
		ContextTokens: ti.ContextTokens,
		TodosDone:     ti.TodosDone,
		TodosTotal:    ti.TodosTotal,
		CurrentTodo:   ti.CurrentTodo,
		Born:          ti.Born,
	}
}

// Adopt is unnecessary for Claude: its SessionStart hook reports the
// conversation ID before the first turn.
func (claudeProvider) Adopt(string, time.Time) string { return "" }

func (claudeProvider) LiveName(sessionID string) string {
	return claudesessions.LiveNames()[sessionID]
}
func (claudeProvider) Scan(exclude map[string]bool, limit int) []Conversation {
	src := claudesessions.Scan(exclude, limit)
	out := make([]Conversation, 0, len(src))
	for _, c := range src {
		out = append(out, Conversation{
			Agent: state.AgentClaude, SessionID: c.SessionID,
			Dir: c.Dir, Title: c.Title, MTime: c.MTime,
		})
	}
	return out
}
func (claudeProvider) Rename(sessionID, name string) bool {
	return claudesessions.AppendRename(sessionID, name) == nil
}
func (claudeProvider) Cost(path string, prev CostState) CostState {
	delta, off, rescanned := claudesessions.CostDelta(path, prev.Offset)
	total := prev.Total + delta
	if rescanned {
		total = delta
	}
	return CostState{Offset: off, Total: total}
}
func (claudeProvider) StatusFromPhase(string) status.Kind { return "" } // hooks own status

// ---- Codex -------------------------------------------------------------

type codexProvider struct{}

func (codexProvider) Kind() string  { return state.AgentCodex }
func (codexProvider) Label() string { return "codex" }
func (codexProvider) Binary() string {
	return lookup("AGENTBOSS_CODEX_CMD", "codex")
}
func (c codexProvider) Installed() bool { return installed(c.Binary()) }
func (codexProvider) ResumeArgs(sessionID string) []string {
	return []string{"resume", sessionID}
}
func (codexProvider) TranscriptPath(sessionID string) string {
	return codexsessions.TranscriptPath(sessionID)
}

// ScratchDir: Codex keeps no per-session working directory — its rollout
// transcript is all it writes — so there is nothing to open.
func (codexProvider) ScratchDir(string) string { return "" }

func (codexProvider) Probe(path string) Info {
	ci := codexsessions.Probe(path)
	return Info{
		Title:         ci.Title,
		TitleExplicit: ci.TitleExplicit,
		Model:         ci.Model,
		Family:        codexFamily(ci.Model),
		ContextTokens: ci.ContextTokens,
		ContextWindow: ci.ContextWindow,
		Born:          ci.Born,
		Phase:         ci.Phase,
	}
}

// Adopt matches the transcript Codex wrote for a session we launched, so its
// name, model and tokens appear without waiting for the first turn to end
// (Codex only reveals a thread ID via notify, at turn end).
func (codexProvider) Adopt(dir string, notBefore time.Time) string {
	id, _ := codexsessions.FindLatest(dir, notBefore)
	return id
}

// LiveName reads Codex's own session index, where an in-CLI rename lands.
func (codexProvider) LiveName(sessionID string) string {
	return codexsessions.Names()[sessionID]
}

func (codexProvider) Scan(exclude map[string]bool, limit int) []Conversation {
	src := codexsessions.Scan(exclude, limit)
	out := make([]Conversation, 0, len(src))
	for _, c := range src {
		out = append(out, Conversation{
			Agent: state.AgentCodex, SessionID: c.SessionID,
			Dir: c.Dir, Title: c.Title, MTime: c.MTime,
		})
	}
	return out
}

// Rename appends to Codex's session index in its own append-only format, so
// the name is what `codex resume` lists too — the same mechanism Codex uses
// when you rename from inside the CLI.
func (codexProvider) Rename(sessionID, name string) bool {
	return codexsessions.SetName(sessionID, name) == nil
}

// Cost is not estimated for Codex: agentboss has no trustworthy price table
// for the models Codex runs, and a wrong number is worse than none.
func (codexProvider) Cost(_ string, prev CostState) CostState { return prev }

func (codexProvider) StatusFromPhase(phase string) status.Kind {
	switch phase {
	case "working":
		return status.Working
	case "done":
		return status.Attention
	case "aborted":
		return status.Idle
	default:
		return ""
	}
}

// codexFamily shortens a Codex model id for the sidebar column:
// "gpt-5.6-sol" → "gpt-5.6".
func codexFamily(model string) string {
	if model == "" {
		return ""
	}
	parts := splitN(model, '-', 3)
	if len(parts) >= 2 && looksVersion(parts[1]) {
		return parts[0] + "-" + parts[1]
	}
	return parts[0]
}

func looksVersion(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}

func splitN(s string, sep byte, n int) []string {
	var out []string
	start := 0
	for i := 0; i < len(s) && len(out) < n-1; i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func installed(bin string) bool {
	if bin == "" {
		return false
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

func envOverride(name string) string { return os.Getenv(name) }

// ShortFamily normalizes a model name spotted in a pane's status bar into
// the compact family label used in the sidebar ("gpt-5.6-sol" → "gpt-5.6").
func ShortFamily(name string) string {
	if strings.HasPrefix(name, "gpt") || strings.HasPrefix(name, "codex") {
		return codexFamily(name)
	}
	return name
}
