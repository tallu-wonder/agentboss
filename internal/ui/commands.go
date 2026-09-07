package ui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tallu-wonder/agentboss/internal/keymap"
	"github.com/tallu-wonder/agentboss/internal/sanitize"
)

func (m *Model) commandItems() []pickItem {
	var items []pickItem
	for _, a := range keymap.Actions {
		if a.Key == "p" {
			continue
		}
		items = append(items, pickItem{id: "alt+" + a.Key, label: a.Label, key: modKey(a.Key)})
	}
	items = append(items,
		pickItem{id: "fork", label: "fork the conversation"},
		pickItem{id: "remove", label: "remove selected sessions from desk"},
		pickItem{id: "filter-agent", label: "filter by agent"},
		pickItem{id: "filter-project", label: "filter by project"},
		pickItem{id: "clear-filters", label: "clear all filters"},
		pickItem{id: "clear-selection", label: "clear selection"})
	var out []pickItem
	for _, it := range items {
		if fuzzyMatch(m.search.Value(), it.label, it.key) {
			out = append(out, it)
		}
	}
	return out
}

func (m *Model) runCommand(it pickItem) tea.Cmd {
	m.mode = modeNormal
	m.search.SetValue(m.filter)
	m.search.Blur()
	switch it.id {
	case "fork":
		if id := m.selectedSessionID(); id != "" {
			m.forkSession(id)
		}
	case "remove":
		m.requestSessionAction("remove", m.actionTargets())
	case "filter-agent", "filter-project":
		m.openFilterKind(it.id)
	case "clear-filters":
		m.clearFilters()
		m.buildRows()
	case "clear-selection":
		m.marked = nil
	default:
		_, cmd := m.keyNormalStr(it.id)
		return cmd
	}
	return nil
}

func (m *Model) keyCommands(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := m.commandItems()
	switch msg.String() {
	case "esc":
		m.mode = modeNormal
		m.search.SetValue(m.filter)
		m.search.Blur()
		return m, nil
	case "up", "ctrl+p":
		m.pickSel = max(0, m.pickSel-1)
		return m, nil
	case "down", "ctrl+n":
		m.pickSel = min(max(0, len(items)-1), m.pickSel+1)
		return m, nil
	case "enter":
		if m.pickSel < len(items) {
			return m, m.runCommand(items[m.pickSel])
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	m.pickSel, m.pickTop = 0, 0
	return m, cmd
}

func (m *Model) commandWindow() (int, int) {
	n := max(1, m.listInnerHeight()-5)
	if m.pickSel < m.pickTop {
		m.pickTop = m.pickSel
	}
	if m.pickSel >= m.pickTop+n {
		m.pickTop = m.pickSel - n + 1
	}
	return m.pickTop, min(len(m.commandItems()), m.pickTop+n)
}

func (m *Model) viewCommands() string {
	items := m.commandItems()
	start, end := m.commandWindow()
	lines := []string{stHeader.Render(" Commands"), " " + m.search.View(), ""}
	for i := start; i < end; i++ {
		it := items[i]
		label := pad(it.label, max(1, m.width-len([]rune(it.key))-4)) + " " + it.key
		if i == m.pickSel {
			label = stSelected.Render(pad("› "+label, m.width))
		} else {
			label = "  " + label
		}
		lines = append(lines, label)
	}
	if len(items) == 0 {
		lines = append(lines, stDim.Render(" No matching commands"))
	}
	for len(lines) < m.listInnerHeight()-1 {
		lines = append(lines, "")
	}
	lines = append(lines, stDim.Render(" ↑↓ choose · Enter run · Esc cancel"))
	return fitLines(lines, m.width, m.listInnerHeight())
}

func (m *Model) mouseCommands(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelDown:
		return m.keyCommands(tea.KeyMsg{Type: tea.KeyDown})
	case tea.MouseButtonWheelUp:
		return m.keyCommands(tea.KeyMsg{Type: tea.KeyUp})
	case tea.MouseButtonLeft:
		idx := m.pickTop + msg.Y - m.listTopY() - 3
		items := m.commandItems()
		if msg.Y >= m.listTopY()+3 && msg.Y < m.height-2 && idx >= 0 && idx < len(items) {
			return m, m.runCommand(items[idx])
		}
	}
	return m, nil
}

func (m *Model) openFilters() { m.openFilterKind("filter") }

func (m *Model) openFilterKind(kind string) {
	m.focusSidebar()
	m.pickKind = kind
	m.mode = modeGroupPick
	m.pickSel, m.pickTop = 0, 0
	m.inputErr = ""
	switch kind {
	case "filter":
		m.pickItems = []pickItem{{id: "all", label: "All sessions · clear filters"}, {id: "attention", label: "Needs attention"}, {id: "agent", label: "Choose agent…"}, {id: "project", label: "Choose project…"}}
	case "filter-agent":
		m.pickItems = []pickItem{{id: "", label: "All agents"}, {id: "claude", label: "Claude Code"}, {id: "codex", label: "Codex"}}
	case "filter-project":
		m.pickItems = []pickItem{{id: "", label: "All projects"}}
		dirs := map[string]bool{}
		for _, s := range m.st.Sessions {
			dirs[s.Dir] = true
		}
		var paths []string
		for dir := range dirs {
			paths = append(paths, dir)
		}
		sort.Strings(paths)
		for _, dir := range paths {
			m.pickItems = append(m.pickItems, pickItem{id: dir, label: sanitize.Line(shortDir(dir))})
		}
	}
}

func (m *Model) applyFilter(id string) {
	switch m.pickKind {
	case "filter-agent":
		m.agentFilter = id
	case "filter-project":
		m.projectFilter = id
	case "filter":
		switch id {
		case "all":
			m.clearFilters()
		case "attention":
			m.attentionOnly = true
		case "agent":
			m.openFilterKind("filter-agent")
			return
		case "project":
			m.openFilterKind("filter-project")
			return
		}
	}
	m.mode = modeNormal
	m.buildRows()
}

func (m *Model) filterLabel() string {
	var parts []string
	if m.attentionOnly {
		parts = append(parts, "Needs attention")
	}
	if m.agentFilter != "" {
		parts = append(parts, m.agentFilter)
	}
	if m.projectFilter != "" {
		parts = append(parts, sanitize.Line(shortDir(m.projectFilter)))
	}
	if m.filter != "" {
		parts = append(parts, "Find: "+m.filter)
	}
	if len(m.marked) > 0 {
		parts = append(parts, "selected: "+fmt.Sprint(len(m.marked)))
	}
	return strings.Join(parts, " · ")
}
