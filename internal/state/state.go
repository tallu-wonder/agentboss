// Package state is the persistent model of the desk: groups and sessions in
// user-chosen order. It is the single source of truth that survives reboots.
// Only the manager process writes it; hook processes write the status dir.
package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tallu-wonder/agentdeck/internal/sanitize"
)

// Group is a named section of the desk ("tools", "reviews", ...).
type Group struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Collapsed bool   `json:"collapsed,omitempty"`
	// Color is a palette slot (see ui.groupPalette); assigned round-robin
	// at creation, cycled with 'c'.
	Color int `json:"color,omitempty"`
}

// Session is one agent conversation tied to a working directory.
type Session struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	GroupID string `json:"group_id,omitempty"` // "" = ungrouped
	// Agent is which CLI runs here: "claude" (default when empty) or "codex".
	Agent string `json:"agent,omitempty"`
	// SessionID is the agent's own conversation ID, used to resume it.
	SessionID    string    `json:"session_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	LastOpenedAt time.Time `json:"last_opened_at,omitempty"`
	// NamedByUser marks a name the user chose explicitly (via rename).
	// Auto-derived names (folder, import title) keep syncing to the Claude
	// conversation's own title until the user renames.
	NamedByUser bool `json:"named_by_user,omitempty"`
	// Archived sessions live in the "old" section at the bottom of the
	// sidebar; opening one restores it to its group.
	Archived bool `json:"archived,omitempty"`
	// NameExplicit records that Name came from an authoritative source
	// (claude's live registry or an explicit rename record). Derived
	// fallback titles (summaries, first messages) never override it.
	NameExplicit bool `json:"name_explicit,omitempty"`
}

// State is the whole desk. Slice order is display order.
type State struct {
	Version  int       `json:"version"`
	Groups   []Group   `json:"groups"`
	Sessions []Session `json:"sessions"`
	// SidebarWidth is the user's preferred sidebar width in columns
	// (0 = default). Updated when the user drags the pane divider.
	SidebarWidth int `json:"sidebar_width,omitempty"`
	// SortMode orders sessions in the sidebar and tab bar:
	// "" / "manual", "status", "recent", "name", "folder".
	SortMode string `json:"sort_mode,omitempty"`
	// OldExpanded remembers whether the archived-sessions section is open.
	NotifyMuted bool `json:"notify_muted,omitempty"` // desktop alerts silenced by the user
	OldExpanded bool `json:"old_expanded,omitempty"`
}

// TmuxName is the tmux session name backing a agentdeck session.
func TmuxName(sessionID string) string { return TmuxPrefix + sessionID }

// TmuxPrefix namespaces agentdeck's tmux sessions, so they are recognizable
// among whatever else a user runs under tmux.
const TmuxPrefix = "adk_"

// SessionIDFromTmux returns the desk-entry ID a tmux session name encodes,
// or "" if the name isn't one of ours.
func SessionIDFromTmux(name string) string {
	if strings.HasPrefix(name, TmuxPrefix) {
		return strings.TrimPrefix(name, TmuxPrefix)
	}
	return ""
}

// NewID returns a short random identifier ("s_3fa9c2" style).
func NewID(prefix string) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b)
}

// idPattern is the shape of every id agentdeck generates. Ids end up as
// filenames under ~/.agentdeck and as tmux session names, so anything else is
// rejected rather than trusted.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidID reports whether id is safe to use as a filename and tmux session
// suffix.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Load reads the state file. A missing file yields an empty desk.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{Version: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("state file %s is corrupt: %w", path, err)
	}
	// Names are cleaned on the way in: this file is editable by hand and may
	// hold anything, and a name reaches both a terminal and tmux.
	for i := range s.Sessions {
		e := &s.Sessions[i]
		if e.Agent == "" {
			e.Agent = AgentClaude
		}
		e.Name = sanitize.Line(e.Name)
		if !ValidID(e.ID) {
			// An id becomes a filename (status/, cmd/); refuse shapes we did
			// not generate rather than let one escape those directories.
			e.ID = NewID("s")
		}
	}
	for i := range s.Groups {
		s.Groups[i].Name = sanitize.Line(s.Groups[i].Name)
	}
	return &s, nil
}

// Agent kinds. An empty Agent field reads as AgentClaude.
const (
	AgentClaude = "claude"
	AgentCodex  = "codex"
)

// AgentOf returns a session's agent kind, defaulting to Claude.
func (s *Session) AgentOf() string {
	if s.Agent == "" {
		return AgentClaude
	}
	return s.Agent
}

// Save writes atomically (temp file + rename) so a crash mid-write can never
// destroy the desk.
func (s *State) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// Session returns a pointer to the session with the given ID, or nil.
func (s *State) Session(id string) *Session {
	for i := range s.Sessions {
		if s.Sessions[i].ID == id {
			return &s.Sessions[i]
		}
	}
	return nil
}

// Group returns a pointer to the group with the given ID, or nil.
func (s *State) Group(id string) *Group {
	for i := range s.Groups {
		if s.Groups[i].ID == id {
			return &s.Groups[i]
		}
	}
	return nil
}

// AddSession appends a new session (at the end of its group's block) and
// returns its ID.
func (s *State) AddSession(name, dir, groupID string) string {
	return s.AddAgentSession(name, dir, groupID, AgentClaude)
}

// AddAgentSession is AddSession with an explicit agent kind.
func (s *State) AddAgentSession(name, dir, groupID, agent string) string {
	if agent == "" {
		agent = AgentClaude
	}
	sess := Session{
		ID:        NewID("s"),
		Name:      name,
		Dir:       dir,
		GroupID:   groupID,
		Agent:     agent,
		CreatedAt: time.Now(),
	}
	// Insert after the last existing member of the group so it lands at the
	// bottom of that section rather than the bottom of the file.
	insert := len(s.Sessions)
	for i := range s.Sessions {
		if s.Sessions[i].GroupID == groupID {
			insert = i + 1
		}
	}
	s.Sessions = append(s.Sessions, Session{})
	copy(s.Sessions[insert+1:], s.Sessions[insert:])
	s.Sessions[insert] = sess
	return sess.ID
}

// DeleteSession removes a session from the desk.
func (s *State) DeleteSession(id string) {
	for i := range s.Sessions {
		if s.Sessions[i].ID == id {
			s.Sessions = append(s.Sessions[:i], s.Sessions[i+1:]...)
			return
		}
	}
}

// AddGroup appends a new group (with the next palette color) and returns
// its ID.
func (s *State) AddGroup(name string) string {
	g := Group{ID: NewID("g"), Name: name, Color: len(s.Groups) % 8}
	s.Groups = append(s.Groups, g)
	return g.ID
}

// DeleteGroup removes a group; its sessions become ungrouped.
func (s *State) DeleteGroup(id string) {
	for i := range s.Groups {
		if s.Groups[i].ID == id {
			s.Groups = append(s.Groups[:i], s.Groups[i+1:]...)
			break
		}
	}
	for i := range s.Sessions {
		if s.Sessions[i].GroupID == id {
			s.Sessions[i].GroupID = ""
		}
	}
}

// SessionsInGroup returns session IDs belonging to a group, in order.
func (s *State) SessionsInGroup(groupID string) []string {
	var out []string
	for i := range s.Sessions {
		if s.Sessions[i].GroupID == groupID {
			out = append(out, s.Sessions[i].ID)
		}
	}
	return out
}

// groupOrder returns display order of group IDs: ungrouped ("") first, then
// declared groups.
func (s *State) groupOrder() []string {
	out := []string{""}
	for _, g := range s.Groups {
		out = append(out, g.ID)
	}
	return out
}

// MoveSession moves a session one step up or down within its group; at the
// group's edge it crosses into the neighboring group (Chrome-tab style).
func (s *State) MoveSession(id string, delta int) {
	if delta != 1 && delta != -1 {
		return
	}
	idx := -1
	for i := range s.Sessions {
		if s.Sessions[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	cur := &s.Sessions[idx]
	members := s.SessionsInGroup(cur.GroupID)
	pos := 0
	for i, m := range members {
		if m == id {
			pos = i
		}
	}
	if npos := pos + delta; npos >= 0 && npos < len(members) {
		// Swap with the neighbor inside the same group.
		other := s.Session(members[npos])
		oidx := -1
		for i := range s.Sessions {
			if s.Sessions[i].ID == other.ID {
				oidx = i
			}
		}
		s.Sessions[idx], s.Sessions[oidx] = s.Sessions[oidx], s.Sessions[idx]
		return
	}
	// Crossing a group boundary: reassign group, keeping position at the
	// facing edge of the neighbor group.
	order := s.groupOrder()
	gpos := 0
	for i, g := range order {
		if g == cur.GroupID {
			gpos = i
		}
	}
	ngpos := gpos + delta
	if ngpos < 0 || ngpos >= len(order) {
		return
	}
	target := order[ngpos]
	cur.GroupID = target
	moved := *cur
	s.Sessions = append(s.Sessions[:idx], s.Sessions[idx+1:]...)
	tmembers := s.SessionsInGroup(target)
	if delta > 0 || len(tmembers) == 0 {
		// Entering the group below: become its first member.
		insert := len(s.Sessions)
		for i := range s.Sessions {
			if s.Sessions[i].GroupID == target {
				insert = i
				break
			}
		}
		s.Sessions = append(s.Sessions, Session{})
		copy(s.Sessions[insert+1:], s.Sessions[insert:])
		s.Sessions[insert] = moved
	} else {
		// Entering the group above: become its last member.
		insert := 0
		for i := range s.Sessions {
			if s.Sessions[i].GroupID == target {
				insert = i + 1
			}
		}
		s.Sessions = append(s.Sessions, Session{})
		copy(s.Sessions[insert+1:], s.Sessions[insert:])
		s.Sessions[insert] = moved
	}
}

// MoveToGroup reassigns a session to a group, placing it last in that group.
func (s *State) MoveToGroup(id, groupID string) {
	sess := s.Session(id)
	if sess == nil || sess.GroupID == groupID {
		return
	}
	moved := *sess
	moved.GroupID = groupID
	s.DeleteSession(id)
	insert := len(s.Sessions)
	for i := range s.Sessions {
		if s.Sessions[i].GroupID == groupID {
			insert = i + 1
		}
	}
	s.Sessions = append(s.Sessions, Session{})
	copy(s.Sessions[insert+1:], s.Sessions[insert:])
	s.Sessions[insert] = moved
}

// MoveSessionNextTo places a session directly before/after another session,
// adopting its group — the primitive behind drag & drop.
func (s *State) MoveSessionNextTo(id, targetID string, after bool) {
	if id == targetID {
		return
	}
	ci, ti := -1, -1
	for i := range s.Sessions {
		switch s.Sessions[i].ID {
		case id:
			ci = i
		case targetID:
			ti = i
		}
	}
	if ci < 0 || ti < 0 {
		return
	}
	moved := s.Sessions[ci]
	moved.GroupID = s.Sessions[ti].GroupID
	s.Sessions = append(s.Sessions[:ci], s.Sessions[ci+1:]...)
	if ti > ci {
		ti--
	}
	pos := ti
	if after {
		pos = ti + 1
	}
	s.Sessions = append(s.Sessions, Session{})
	copy(s.Sessions[pos+1:], s.Sessions[pos:])
	s.Sessions[pos] = moved
}

// MoveToGroupFront reassigns a session to a group as its first member —
// used when a drag drops onto a group header.
func (s *State) MoveToGroupFront(id, groupID string) {
	sess := s.Session(id)
	if sess == nil {
		return
	}
	moved := *sess
	moved.GroupID = groupID
	s.DeleteSession(id)
	insert := len(s.Sessions)
	for i := range s.Sessions {
		if s.Sessions[i].GroupID == groupID {
			insert = i
			break
		}
	}
	s.Sessions = append(s.Sessions, Session{})
	copy(s.Sessions[insert+1:], s.Sessions[insert:])
	s.Sessions[insert] = moved
}

// MoveGroup shifts a whole group up or down among groups.
func (s *State) MoveGroup(id string, delta int) {
	idx := -1
	for i := range s.Groups {
		if s.Groups[i].ID == id {
			idx = i
		}
	}
	n := idx + delta
	if idx < 0 || n < 0 || n >= len(s.Groups) {
		return
	}
	s.Groups[idx], s.Groups[n] = s.Groups[n], s.Groups[idx]
}
