package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/tallu-wonder/agentboss/internal/agents"
	"github.com/tallu-wonder/agentboss/internal/sanitize"
)

func (m *Model) inputMode() bool {
	return m.mode == modeInputDir || m.mode == modeInputGroup || m.mode == modeRename || m.mode == modeInputWtName
}

func validWorktreeName(v string) bool {
	return wtNameRe.MatchString(v) && !strings.Contains(v, "..") && !strings.HasSuffix(v, "/")
}

func (m *Model) updateSuggestions(recent bool) {
	m.suggestions = nil
	m.suggestionSel = -1
	if m.mode != modeInputDir {
		return
	}
	if recent {
		sessions := append(m.st.Sessions[:0:0], m.st.Sessions...)
		sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].LastOpenedAt.After(sessions[j].LastOpenedAt) })
		seen := map[string]bool{}
		for _, s := range sessions {
			if seen[s.Dir] {
				continue
			}
			seen[s.Dir] = true
			if fi, err := os.Stat(s.Dir); err == nil && fi.IsDir() {
				m.suggestions = append(m.suggestions, s.Dir)
			}
			if len(m.suggestions) == 8 {
				break
			}
		}
		return
	}
	dir, prefix := filepath.Split(expandHome(m.input.Value()))
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		m.suggestions = append(m.suggestions, filepath.Join(dir, e.Name())+"/")
	}
}

func (m *Model) completeDirectory() {
	if m.suggestionSel >= 0 && m.suggestionSel < len(m.suggestions) {
		m.input.SetValue(m.suggestions[m.suggestionSel])
		m.input.CursorEnd()
		m.updateSuggestions(false)
		return
	}
	m.updateSuggestions(false)
	if len(m.suggestions) == 0 {
		m.inputErr = "No matching folders. Check the path or choose a recent folder."
		return
	}
	lcp := []rune(m.suggestions[0])
	for _, candidate := range m.suggestions[1:] {
		r := []rune(candidate)
		n := 0
		for n < len(lcp) && n < len(r) && lcp[n] == r[n] {
			n++
		}
		lcp = lcp[:n]
	}
	m.input.SetValue(string(lcp))
	m.input.CursorEnd()
	m.inputErr = ""
	if len(m.suggestions) == 1 {
		m.updateSuggestions(false)
	}
}

func (m *Model) handleInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "alt+down":
		m.scroll++
		return m, nil
	case "alt+up":
		m.scroll = max(0, m.scroll-1)
		return m, nil
	case "esc":
		m.mode = modeNormal
		m.input.Blur()
		m.pickFor = ""
		m.moveTargets = nil
		m.wtFlow = false
		m.inputErr = ""
		m.pendingDir = ""
		return m, nil
	case "alt+left", "shift+tab":
		if m.mode == modeInputWtName {
			m.startInput(modeInputDir, "repository", m.wtRepo)
		}
		return m, nil
	case "up":
		if m.suggestionSel >= 0 {
			m.suggestionSel--
		}
		return m, nil
	case "down":
		if m.suggestionSel+1 < len(m.suggestions) {
			m.suggestionSel++
		}
		return m, nil
	case "tab":
		if m.mode == modeInputDir {
			m.completePath()
		}
		return m, nil
	case "enter":
		if m.mode == modeInputDir && m.suggestionSel >= 0 && m.suggestionSel < len(m.suggestions) {
			m.input.SetValue(m.suggestions[m.suggestionSel])
		}
		m.inputErr = ""
		return m.submitInput(strings.TrimSpace(m.input.Value()))
	}
	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if before != m.input.Value() {
		m.inputErr = ""
		m.updateSuggestions(false)
	}
	return m, cmd
}

func (m *Model) backToInput() {
	if m.wtFlow {
		m.startInput(modeInputWtName, "worktree name", m.wtName)
	} else {
		m.startInput(modeInputDir, "directory", m.pendingDir)
	}
}

func (m *Model) finishSession(agent string) (tea.Model, tea.Cmd) {
	if !agents.Get(agent).Installed() {
		m.inputErr = agents.Get(agent).Label() + " is not installed. Choose an installed agent or cancel."
		return m, nil
	}
	dir := m.pendingDir
	if m.wtFlow {
		var err error
		dir, err = addWorktree(m.wtRepo, m.wtName)
		if err != nil {
			m.inputErr = err.Error()
			return m, nil
		}
	}
	m.wtFlow = false
	m.pendingDir = ""
	m.inputErr = ""
	m.mode = modeNormal
	if dir != "" {
		m.clearFilters()
		m.createSession(dir, agent)
	}
	return m, nil
}

func (m *Model) viewInput() string {
	w := max(8, m.width-6)
	title, label := "New session · 1/2", "Directory"
	switch m.mode {
	case modeInputDir:
		if m.wtFlow {
			title, label = "New worktree · 1/3", "Repository"
		}
	case modeInputWtName:
		title, label = "New worktree · 2/3", "Branch / worktree name"
	case modeInputGroup:
		title, label = "New group", "Group name"
	case modeRename:
		title, label = "Rename", "Name"
	}
	lines := []string{stHeader.Render(title), "", stText.Render(label), m.input.View()}
	if m.mode == modeInputDir {
		if g := m.st.Group(m.contextGroup()); g != nil {
			lines = append(lines, stDim.Render("Group: "+g.Name))
		}
	}
	if m.mode == modeInputWtName {
		lines = append(lines, stDim.Render("Repository: "+sanitize.Line(shortDir(m.wtRepo))))
	}
	if m.inputErr != "" {
		lines = append(lines, "", stErr.Render(ansi.Wrap(m.inputErr, w, "")))
	} else if m.mode == modeInputDir && len(m.suggestions) > 0 {
		heading := "Matching folders · ↑↓ choose"
		if m.input.Value() == m.inputInitial {
			heading = "Recent folders · ↑↓ choose"
		}
		lines = append(lines, "", stDim.Render(heading))
		visible := min(4, max(0, m.listInnerHeight()-12))
		start := max(0, m.suggestionSel-visible+1)
		for i := start; i < len(m.suggestions) && i < start+visible; i++ {
			text := "  " + sanitize.Line(shortDir(m.suggestions[i]))
			if i == m.suggestionSel {
				text = stSelected.Render(pad("› "+sanitize.Line(shortDir(m.suggestions[i])), w))
			}
			lines = append(lines, text)
		}
	}
	hint := "Enter next · Esc cancel"
	if m.mode == modeInputGroup || m.mode == modeRename {
		hint = "Enter save · Esc cancel"
	}
	if m.mode == modeInputDir {
		hint = "Tab complete · Enter next · Esc cancel"
	}
	if m.mode == modeInputWtName {
		hint = "Shift+Tab back · Enter next · Esc cancel"
	}
	if m.inputErr != "" {
		hint = "Alt+↑↓ error · Enter retry · Esc cancel"
	}
	// Field and errors stay on screen; long diagnostics scroll with Alt+Up/Down.
	return m.scrollBox(lines, hint)
}

func (m *Model) pickerVisible() int { return max(1, m.listInnerHeight()-9) }

func (m *Model) pickerWindow() (int, int) {
	n := m.pickerVisible()
	if m.pickSel < m.pickTop {
		m.pickTop = m.pickSel
	}
	if m.pickSel >= m.pickTop+n {
		m.pickTop = m.pickSel - n + 1
	}
	m.pickTop = max(0, min(m.pickTop, max(0, len(m.pickItems)-n)))
	return m.pickTop, min(len(m.pickItems), m.pickTop+n)
}

func (m *Model) pickerHint() string {
	if m.inputErr != "" {
		return "Alt+↑↓ error · ← back · Esc cancel"
	}
	if m.pickKind == "agent" {
		return "← back · Enter start · Esc cancel"
	}
	return "↑↓ choose · Enter select · Esc cancel"
}

func (m *Model) pickerTitle() string {
	switch m.pickKind {
	case "agent":
		if m.wtFlow {
			return "New worktree · 3/3 · Agent"
		}
		return "New session · 2/2 · Agent"
	case "group":
		if len(m.moveTargets) > 1 {
			return fmt.Sprintf("Move %d sessions", len(m.moveTargets))
		}
		return "Move to group"
	case "sort":
		return "Sort sessions"
	case "color":
		return "Group color"
	case "filter":
		return "Filter sessions"
	case "filter-agent":
		return "Filter by agent"
	case "filter-project":
		return "Filter by project"
	case "menu":
		if m.menuRow.kind == rowGroup {
			if g := m.st.Group(m.menuRow.id); g != nil {
				return g.Name
			}
			return "Archived"
		}
		if s := m.st.Session(m.menuRow.id); s != nil {
			return s.Name
		}
	}
	return "Choose"
}
