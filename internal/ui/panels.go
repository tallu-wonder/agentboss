package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/tallu-wonder/agentboss/internal/keymap"
	"github.com/tallu-wonder/agentboss/internal/sanitize"
	"github.com/tallu-wonder/agentboss/internal/status"
)

func fitLines(lines []string, w, h int) string {
	for len(lines) < h {
		lines = append(lines, "")
	}
	lines = lines[:max(0, h)]
	for i := range lines {
		lines[i] = pad(lines[i], w)
	}
	return strings.Join(lines, "\n")
}

// The footer stays visible while the content scrolls. Never crop an action's
// consequence or discard information just because the sidebar is narrow.
func (m *Model) scrollBox(lines []string, hint string) string {
	w := max(8, m.width-6)
	var content []string
	for _, line := range lines {
		content = append(content, strings.Split(ansi.Wrap(line, w, ""), "\n")...)
	}
	foot := strings.Split(ansi.Wrap(hint, w, ""), "\n")
	visible := max(1, m.listInnerHeight()-len(foot)-3)
	m.scroll = max(0, min(m.scroll, max(0, len(content)-visible)))
	end := min(len(content), m.scroll+visible)
	shown := append([]string{}, content[m.scroll:end]...)
	if len(content) > visible {
		shown = append(shown, stDim.Render(fmt.Sprintf("↑↓ %d–%d / %d", m.scroll+1, end, len(content))))
	} else {
		shown = append(shown, "")
	}
	for _, f := range foot {
		shown = append(shown, stDim.Render(f))
	}
	for i := range shown {
		shown[i] = pad(shown[i], w)
	}
	return stOverlay.Render(strings.Join(shown, "\n"))
}

func (m *Model) keyScroll(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeNormal
		m.scroll = 0
	case "up":
		m.scroll = max(0, m.scroll-1)
	case "down":
		m.scroll++
	case "pgup":
		m.scroll = max(0, m.scroll-max(1, m.listInnerHeight()-4))
	case "pgdown":
		m.scroll += max(1, m.listInnerHeight()-4)
	case "home":
		m.scroll = 0
	case "end":
		m.scroll = 1 << 20
	}
	return m, nil
}

func (m *Model) viewFocus() string {
	text := "Typing into: sidebar"
	if m.unfocused {
		if s := m.st.Session(m.activeID); s != nil {
			agent := " · " + s.AgentOf()
			text = "Typing: " + pad(s.Name, max(1, m.width-9-ansi.StringWidth(agent))) + agent
		} else {
			text = "Typing into: session pane"
		}
	}
	return pad(stActive.Render(" "+text), m.width)
}

func (m *Model) viewFilterBar() string {
	if label := m.filterLabel(); label != "" {
		return pad(stNotice.Render(" "+label+" · Esc clear"), m.width)
	}
	return stFaint.Render(strings.Repeat("─", m.width))
}

func (m *Model) listRowsHeight() int {
	h := m.listInnerHeight()
	if m.height >= 16 {
		h -= 4
	}
	return max(1, h)
}

func (m *Model) rowHeight(r row) int {
	if m.attentionOnly && r.kind == rowSession {
		return 2
	}
	return 1
}

func (m *Model) rowAt(y int) int {
	y -= m.listTopY()
	if y < 0 || y >= m.listRowsHeight() {
		return -1
	}
	for i := m.top; i < len(m.rows); i++ {
		y -= m.rowHeight(m.rows[i])
		if y < 0 {
			return i
		}
	}
	return -1
}

func (m *Model) updateBranch() {
	s := m.st.Session(m.selectedSessionID())
	if s == nil {
		return
	}
	if s.Dir != m.branchDir || time.Since(m.branchChecked) > 3*time.Second {
		m.branchDir = s.Dir
		m.branchChecked = time.Now()
		m.branchValue = gitInfo(s.Dir)
	}
}

func (m *Model) contextLabel(id string) string {
	if value := m.contextValue(id); value != "" {
		return "ctx " + value
	}
	return ""
}

func (m *Model) contextValue(id string) string {
	tokens := m.tokensOf(id)
	if tokens <= 0 {
		return ""
	}
	window := m.contextWindowOf(id)
	if window <= 0 {
		return fmtTokens(tokens)
	}
	return fmt.Sprintf("%d%%", tokens*100/window)
}

func (m *Model) selectedDetails() []string {
	lines := []string{stFaint.Render(strings.Repeat("─", m.width)), "", "", ""}
	s := m.st.Session(m.selectedSessionID())
	if s == nil {
		return lines
	}
	lines[1] = stText.Render("Selected: " + s.Name)
	project := "Project: " + shortProject(s.Dir)
	if m.branchDir == s.Dir && m.branchValue != "" {
		project += " · " + m.branchValue
	}
	lines[2] = stDim.Render(project)
	parts := []string{s.AgentOf()}
	if f := m.familyOf(s.ID); f != "" {
		parts = append(parts, f)
	}
	if ctx := m.contextLabel(s.ID); ctx != "" {
		parts = append(parts, ctx)
	}
	if cost := m.costOf(s.ID); cost >= .01 {
		parts = append(parts, "est "+fmtUSD(cost))
	}
	lines[3] = stDim.Render(strings.Join(parts, " · "))
	return lines
}

func (m *Model) attentionReason(id string) string {
	r := m.runtime[id]
	reason := r.Message
	if pos := strings.Index(strings.ToLower(reason), "permission to use "); pos >= 0 {
		reason = "Permission: " + reason[pos+len("permission to use "):]
	}
	if reason == "" {
		if m.statusOf(id) == status.NeedsYou {
			reason = "Waiting for your response"
		} else {
			reason = "Finished since you last looked"
		}
	}
	wait := ago(r.UpdatedAt)
	if wait == "now" {
		wait = "<1m"
	}
	if m.statusOf(id) == status.NeedsYou {
		wait = "waiting " + wait
	}
	return stDim.Render("  " + wait + " · " + sanitize.Line(reason))
}

func (m *Model) helpLines() []string {
	lines := []string{stHeader.Render("Keys and status"), ""}
	rows := [][2]string{{"Enter", "open / resume selected session"}, {"Tab", "focus session"}, {"Ctrl+\\", "switch keyboard focus"}, {modKey("[") + " " + modKey("]"), "previous / next session"}, {modKey("1") + "–" + modKey("9"), "open session by tab number"}, {modKey("a"), "next session needing attention"}}
	for _, a := range keymap.Actions {
		rows = append(rows, [2]string{modKey(a.Key), a.Label})
	}
	for _, r := range rows {
		lines = append(lines, stText.Bold(true).Render(r[0])+"  "+r[1])
	}
	lines = append(lines, "", "Mouse: right-click for actions; middle-click asks to stop.", "", "◆ needs you: blocked until the agent resumes", "● new: finished since you last looked", "▶ working · ⏸ idle · ■ stopped / resumable", "", "Search: agent:codex project:payments status:blocked", "Select: "+modKey("b")+" toggles; "+modKey("B")+" selects visible.", "Selected sessions stay selected across filters; Esc clears selection first.", "Move or archive applies to all selected sessions.", "", "Bare letters never act outside a dialog.", "Option must send Alt: Ghostty macos-option-as-alt = true; iTerm2 Option = Esc+.", "tmux prefix: Ctrl+Q")
	return lines
}

func shortProject(dir string) string { return sanitize.Line(filepath.Base(dir)) }
