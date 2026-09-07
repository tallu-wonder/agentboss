package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tallu-wonder/agentboss/internal/state"
	"github.com/tallu-wonder/agentboss/internal/status"
	"github.com/tallu-wonder/agentboss/internal/tmuxctl"
)

type undoSession struct {
	ID             string
	Archived, Live bool
}

func (m *Model) unseenStatus(id string, k status.Kind, at time.Time) status.Kind {
	if s := m.st.Session(id); s != nil && k == status.Attention && !s.SeenAt.IsZero() && !at.After(s.SeenAt) {
		return status.Idle
	}
	return k
}

func (m *Model) actionTargets() []string {
	var ids []string
	for _, s := range m.st.Sessions {
		if m.marked[s.ID] {
			ids = append(ids, s.ID)
		}
	}
	if len(ids) == 0 {
		if id := m.selectedSessionID(); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (m *Model) toggleMarked() {
	id := m.selectedSessionID()
	if id == "" {
		return
	}
	if m.marked == nil {
		m.marked = map[string]bool{}
	}
	if m.marked[id] {
		delete(m.marked, id)
	} else {
		m.marked[id] = true
	}
}

func (m *Model) selectVisible() {
	if m.marked == nil {
		m.marked = map[string]bool{}
	}
	for _, r := range m.rows {
		if r.kind == rowSession {
			m.marked[r.id] = true
		}
	}
}

// All entry points use the same confirmation, including tab middle-clicks.
// Capture IDs now: a filter or status refresh cannot change the approved targets.
func (m *Model) requestSessionAction(action string, targets []string) {
	var ids, names []string
	for _, id := range targets {
		s := m.st.Session(id)
		if s == nil || action == "stop" && !m.isLive(id) || action == "archive" && s.Archived {
			continue
		}
		ids = append(ids, id)
		names = append(names, s.Name)
	}
	if len(ids) == 0 {
		m.flash("no sessions available for this action", false)
		return
	}
	title, effect := "Stop session?", "Stops the agent process. Conversation stays on the desk; Enter resumes it."
	switch action {
	case "archive":
		title, effect = "Archive session?", "Stops the agent process and moves the entry to Archived. The conversation can be resumed."
	case "remove":
		title, effect = "Remove from desk?", "Stops the agent process and removes the desk entry. Conversation transcripts and project files remain."
	}
	if len(ids) > 1 {
		title = strings.TrimSuffix(title, " session?") + fmt.Sprintf(" %d sessions?", len(ids))
		if action == "remove" {
			title = fmt.Sprintf("Remove %d sessions from desk?", len(ids))
		}
	}
	m.confirm(title+"\n\n"+effect+"\n\n"+strings.Join(names, "\n"), func() tea.Cmd {
		var undo []undoSession
		failed := 0
		for _, id := range ids {
			s := m.st.Session(id)
			if s == nil {
				continue
			}
			u := undoSession{id, s.Archived, m.isLive(id)}
			ok := false
			switch action {
			case "stop":
				ok = m.sleep(id)
			case "archive":
				ok = m.archive(id)
			case "remove":
				ok = m.deleteSession(id)
			}
			if !ok {
				failed++
				continue
			}
			delete(m.marked, id)
			if action != "remove" {
				undo = append(undo, u)
			}
		}
		if len(undo) > 0 {
			m.undoSessions = undo
			m.lastClosed = undo[len(undo)-1].ID
		}
		if !m.save() {
			return nil
		}
		m.buildRows()
		if failed > 0 {
			m.flash(fmt.Sprintf("%d failed; failed entries are unchanged", failed), true)
		} else {
			label := map[string]string{"stop": "stopped", "archive": "archived", "remove": "removed from desk"}[action]
			msg := fmt.Sprintf("%d %s", len(ids), label)
			if action != "remove" {
				msg += " · " + modKey("u") + " undo"
			}
			m.flash(msg, false)
		}
		return nil
	})
}

func (m *Model) undoLastAction() {
	items := m.undoSessions
	if len(items) == 0 && m.lastClosed != "" {
		items = []undoSession{{ID: m.lastClosed, Live: true}}
	}
	if len(items) == 0 {
		m.flash("nothing to undo", false)
		return
	}
	var failed []undoSession
	last := ""
	for _, u := range items {
		s := m.st.Session(u.ID)
		if s == nil {
			continue
		}
		if u.Live && !m.isLive(u.ID) {
			if err := m.wake(s); err != nil {
				failed = append(failed, u)
				continue
			}
		}
		s.Archived = u.Archived
		last = u.ID
	}
	m.undoSessions = failed
	m.lastClosed = ""
	m.clearFilters()
	m.save()
	m.buildRows()
	if last != "" {
		m.selectSession(last)
		if m.isLive(last) {
			m.open(last, false)
		}
	}
	if len(failed) > 0 {
		m.flash(fmt.Sprintf("%d could not resume; %s retries", len(failed), modKey("u")), true)
	}
}

func (m *Model) moveSessions(gid string) {
	ids := m.moveTargets
	if len(ids) == 0 && m.pickFor != "" {
		ids = []string{m.pickFor}
	}
	for _, id := range ids {
		m.st.MoveToGroup(id, gid)
		if s := m.st.Session(id); s != nil {
			s.Archived = false
		}
		delete(m.marked, id)
	}
	if g := m.st.Group(gid); g != nil {
		g.Collapsed = false
	}
	m.moveTargets = nil
	m.pickFor = ""
}

func (m *Model) focusSidebar() {
	if m.sidebarPane != "" {
		_ = tmuxctl.SelectPane(m.sidebarPane)
	}
	m.unfocused = false
}

// A global action must target the session on screen even when filters or a
// collapsed group currently hide its row.
func (m *Model) revealSession(id string) {
	s := m.st.Session(id)
	if s == nil {
		return
	}
	for _, r := range m.rows {
		if r.kind == rowSession && r.id == id {
			m.selectSession(id)
			return
		}
	}
	m.clearFilters()
	if g := m.st.Group(s.GroupID); g != nil {
		g.Collapsed = false
		m.dirty = true
	}
	if s.Archived {
		m.st.OldExpanded = true
		m.dirty = true
	}
	m.buildRows()
	m.selectSession(id)
}

// Tab navigation uses the same sorted order as the rendered number badges.
// Filters affect the list, never the meaning of an open tab's number.
func (m *Model) navigateTabs(action string) {
	ids := m.numberedLive()
	if len(ids) == 0 {
		return
	}
	idx := -1
	for i, id := range ids {
		if id == m.activeID {
			idx = i
			break
		}
	}
	switch {
	case action == "next":
		idx = (idx + 1) % len(ids)
	case action == "prev":
		idx = (idx - 1 + len(ids)) % len(ids)
	case len(action) == 2 && action[0] == 'n' && action[1] >= '1' && action[1] <= '9':
		idx = int(action[1] - '1')
	default:
		return
	}
	if idx < 0 || idx >= len(ids) {
		return
	}
	m.revealSession(ids[idx])
	m.open(ids[idx], false)
}

func (m *Model) clearFilters() {
	m.filter, m.agentFilter, m.projectFilter = "", "", ""
	m.attentionOnly = false
	m.search.SetValue("")
}

func (m *Model) filtersActive() bool {
	return m.filter != "" || m.attentionOnly || m.agentFilter != "" || m.projectFilter != ""
}

func (m *Model) matchesSession(s *state.Session, group string) bool {
	k := m.statusOf(s.ID)
	if m.attentionOnly && (s.Archived || k != status.NeedsYou && k != status.Attention) {
		return false
	}
	if m.agentFilter != "" && m.agentFilter != s.AgentOf() {
		return false
	}
	if m.projectFilter != "" && m.projectFilter != s.Dir {
		return false
	}
	var words []string
	for _, term := range strings.Fields(m.filter) {
		key, value, ok := strings.Cut(strings.ToLower(term), ":")
		if ok && value != "" {
			switch key {
			case "agent":
				if !strings.Contains(s.AgentOf(), value) {
					return false
				}
				continue
			case "project":
				if !strings.Contains(strings.ToLower(s.Dir), value) {
					return false
				}
				continue
			case "status":
				match := strings.ReplaceAll(string(k), "_", "-") == value
				if value == "blocked" {
					match = k == status.NeedsYou
				}
				if value == "attention" {
					match = k == status.NeedsYou || k == status.Attention
				}
				if !match {
					return false
				}
				continue
			}
		}
		words = append(words, term)
	}
	return fuzzyMatch(strings.Join(words, " "), s.Name, s.Dir+" "+group)
}
