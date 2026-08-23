package ui

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/tallu-wonder/agentboss/internal/state"
	"github.com/tallu-wonder/agentboss/internal/status"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// optName is what this platform calls the Alt modifier — the key legend must
// name the key on the user's keyboard, not tmux's name for it.
func optName() string {
	if runtime.GOOS == "darwin" {
		return "opt"
	}
	return "alt"
}

// modKey renders a chord for the key legend: compact ⌥ on macOS, spelled out
// elsewhere.
func modKey(k string) string {
	if runtime.GOOS == "darwin" {
		return "⌥" + k
	}
	return "alt+" + k
}

// groupPalette maps a group's color slot to matching terminal colors for
// the sidebar (lipgloss) and the tab bar (tmux formats).
var groupPalette = []struct {
	lip  lipgloss.Color
	tmux string
}{
	{lipgloss.Color("75"), "colour75"},   // blue
	{lipgloss.Color("114"), "colour114"}, // green
	{lipgloss.Color("179"), "colour179"}, // yellow
	{lipgloss.Color("211"), "colour211"}, // pink
	{lipgloss.Color("140"), "colour140"}, // purple
	{lipgloss.Color("80"), "colour80"},   // cyan
	{lipgloss.Color("215"), "colour215"}, // orange
	{lipgloss.Color("167"), "colour167"}, // red
}

// groupColorNames matches groupPalette by index, for the color picker.
var groupColorNames = []string{"blue", "green", "yellow", "pink", "purple", "cyan", "orange", "red"}

func groupSlot(color int) int {
	if color < 0 {
		color = -color
	}
	return color % len(groupPalette)
}

func groupLip(color int) lipgloss.Color { return groupPalette[groupSlot(color)].lip }
func groupTmux(color int) string        { return groupPalette[groupSlot(color)].tmux }

// palette
var (
	cAccent  = lipgloss.Color("6")   // cyan
	cWorking = lipgloss.Color("14")  // bright cyan
	cAlert   = lipgloss.Color("203") // red-ish: needs you
	cNew     = lipgloss.Color("221") // yellow: finished, unseen
	cDim     = lipgloss.Color("242")
	cFaint   = lipgloss.Color("238")
	cText    = lipgloss.Color("252")
	cSelBg   = lipgloss.Color("237")

	stHeader   = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	stWorking  = lipgloss.NewStyle().Foreground(cWorking)
	stAlert    = lipgloss.NewStyle().Foreground(cAlert).Bold(true)
	stNew      = lipgloss.NewStyle().Foreground(cNew)
	stIdle     = lipgloss.NewStyle().Foreground(cDim)
	stDormant  = lipgloss.NewStyle().Foreground(cFaint)
	stText     = lipgloss.NewStyle().Foreground(cText)
	stDim      = lipgloss.NewStyle().Foreground(cDim)
	stFaint    = lipgloss.NewStyle().Foreground(cFaint)
	stErr      = lipgloss.NewStyle().Foreground(cAlert)
	stNotice   = lipgloss.NewStyle().Foreground(cNew)
	stActive   = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	stOverlay  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(0, 1)
	stSelected = lipgloss.NewStyle().Background(cSelBg)
)

// ---- layout metrics ------------------------------------------------------

// listTopY is the first screen row of list content (header + rule above).
func (m *Model) listTopY() int { return 2 }

func (m *Model) listInnerHeight() int { return m.height - 3 } // header, rule, footer

// ---- helpers ---------------------------------------------------------

// Widths of the fixed right-hand columns. "$1600" and "1.2M" are the widest
// values each can hold.
const (
	costW  = 6
	modelW = 7
	tokenW = 4
	ageW   = 3
)

// blank is an empty cell that still occupies its column.
func blank(w int) string { return strings.Repeat(" ", w) }

// padNum right-aligns s in w cells, so digits line up down a column.
func padNum(s string, w int) string {
	if gap := w - ansi.StringWidth(s); gap > 0 {
		return strings.Repeat(" ", gap) + s
	}
	return ansi.Truncate(s, w, "")
}

// pad truncates/pads s (which may contain ANSI escapes) to exactly w cells.
func pad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if gap := w - ansi.StringWidth(s); gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// agoLong renders a human "3m ago" / "just now".
func agoLong(t time.Time) string {
	a := ago(t)
	if a == "now" {
		return "just now"
	}
	return a + " ago"
}

// statusGlyph returns the icon and its style for a status kind.
func (m *Model) statusGlyph(k status.Kind) (string, lipgloss.Style) {
	switch k {
	case status.Working:
		return spinnerFrames[m.spin%len(spinnerFrames)], stWorking
	case status.NeedsYou:
		return "◆", stAlert
	case status.Attention:
		return "●", stNew
	case status.Idle:
		return "·", stIdle
	default:
		return "○", stDormant
	}
}

// ---- top-level view --------------------------------------------------

func (m *Model) View() string {
	if m.width < 16 || m.height < 6 {
		return "pane too small"
	}
	var body string
	switch m.mode {
	case modeHelp:
		body = m.viewHelp()
	case modeInfo:
		body = m.overlay(m.viewInfo())
	case modeGroupPick:
		body = m.overlay(m.viewGroupPick())
	case modeConfirm:
		body = m.overlay(m.viewConfirm())
	case modeImport:
		body = m.viewImport()
	default:
		body = m.viewList()
	}
	return m.viewHeader() + "\n" +
		stFaint.Render(strings.Repeat("─", m.width)) + "\n" +
		body + "\n" +
		m.viewFooter()
}

// overlay floats a box over the session list rather than replacing it — a
// confirm for "delete forever?" must not hide the very row it is about.
func (m *Model) overlay(box string) string {
	// Clamp the box to the pane so a small window never smears the frame.
	lines := strings.Split(box, "\n")
	h := m.listInnerHeight()
	if len(lines) > h {
		lines = lines[:h]
	}
	boxW := 0
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "")
		if w := ansi.StringWidth(lines[i]); w > boxW {
			boxW = w
		}
	}
	bg := strings.Split(m.viewList(), "\n") // exactly h lines, padded to width
	x := (m.width - boxW) / 2
	if x < 0 {
		x = 0
	}
	y := (h - len(lines)) / 2
	for i, bl := range lines {
		if y+i < 0 || y+i >= len(bg) {
			continue
		}
		row := bg[y+i]
		left := ansi.Truncate(row, x, "")
		right := ansi.TruncateLeft(row, x+ansi.StringWidth(bl), "")
		// The cuts can leave a style open; reset so the box and the remnant
		// start clean.
		bg[y+i] = left + "\x1b[0m" + pad(bl, boxW) + "\x1b[0m" + right
	}
	return strings.Join(bg, "\n")
}

// ---- header / footer ---------------------------------------------------

func (m *Model) viewHeader() string {
	working, needs, attn := m.counts()
	title := stHeader.Render("agentboss")
	if m.unfocused {
		// Keys are going to the agent right now; a dimmed title is the cue.
		title = stDim.Render("agentboss")
	}
	left := " " + title + " "
	parts := []string{}
	if working > 0 {
		parts = append(parts, stWorking.Render(fmt.Sprintf("%s%d", spinnerFrames[m.spin%len(spinnerFrames)], working)))
	}
	if needs > 0 {
		parts = append(parts, stAlert.Render(fmt.Sprintf("◆%d", needs)))
	}
	if attn > 0 {
		parts = append(parts, stNew.Render(fmt.Sprintf("●%d", attn)))
	}
	mid := strings.Join(parts, " ")
	right := stDim.Render("? ")
	if m.st.NotifyMuted {
		// Silenced alerts must be visible somewhere, or a quiet desk looks broken.
		right = stDim.Render("🔇 ") + right
	}
	if m.filter != "" && m.mode == modeNormal {
		shown, total := m.matchCount()
		right = stNotice.Render("⌕"+m.filter) +
			stDim.Render(fmt.Sprintf(" %d/%d ", shown, total)) + right
	}
	gap := m.width - ansi.StringWidth(left+mid) - ansi.StringWidth(right)
	if gap < 1 {
		return pad(left+mid, m.width)
	}
	return left + mid + strings.Repeat(" ", gap) + right
}

func (m *Model) viewFooter() string {
	var s string
	switch m.mode {
	case modeSearch:
		s = " " + stHeader.Render("/") + m.search.View()
		// No count until a query exists: with nothing typed, collapsed groups
		// keep sessions out of the rows and the number would read as broken.
		if strings.TrimSpace(m.filter) != "" {
			shown, total := m.matchCount()
			s += stDim.Render(fmt.Sprintf(" %d/%d", shown, total))
		}
	case modeInputDir, modeInputGroup, modeRename:
		s = " " + m.inputLabel() + " " + m.input.View()
	case modeConfirm:
		s = "" // the popup itself says y/n; a second copy just pulls the eye
	case modeImport:
		s = " " + stDim.Render("enter add · esc cancel")
	default:
		switch {
		case m.notice != "":
			st := stNotice
			if m.noticeErr {
				st = stErr
			}
			s = " " + st.Render(m.notice)
		case m.unfocused:
			s = " " + stDim.Render(fitHints(m.width-2, "keys go to the agent", "ctrl+\\ returns"))
		default:
			s = " " + stDim.Render(fitHints(m.width-2, m.footerHints()...))
		}
	}
	return pad(s, m.width)
}

// matchCount is how many sessions the active filter lets through, out of all
// sessions on the desk (old shelf included — the filter searches it too).
func (m *Model) matchCount() (shown, total int) {
	for _, r := range m.rows {
		if r.kind == rowSession {
			shown++
		}
	}
	return shown, len(m.st.Sessions)
}

// footerHints picks the hints for what is actually selected — a group header
// answers to different keys than a session, and an old session's enter means
// something you should know about before pressing it.
func (m *Model) footerHints() []string {
	r := m.selectedRow()
	help := modKey("?") + " keys"
	switch {
	case r != nil && r.kind == rowGroup && r.id == oldSection:
		return []string{"↵ open the shelf", help}
	case r != nil && r.kind == rowGroup:
		return []string{"↵ collapse", modKey("r") + " rename", modKey("c") + " color", help}
	case r != nil:
		if s := m.st.Session(r.id); s != nil && s.Archived {
			return []string{"↵ revive", modKey("x") + " delete", modKey("m") + " regroup", help}
		}
	}
	return []string{"↵ open", modKey("n") + " new", modKey("i") + " import",
		modKey("/") + " find", modKey("s") + " sort", help}
}

// fitHints joins hint segments with separators, dropping middle segments that
// would not fit — the footer never shows a chopped word. The LAST segment is
// kept whatever happens: it is "? keys", the door to all the others.
func fitHints(w int, parts ...string) string {
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	out := ""
	for _, p := range parts[:len(parts)-1] {
		cand := p
		if out != "" {
			cand = out + " · " + p
		}
		if ansi.StringWidth(cand+" · "+last) > w {
			break
		}
		out = cand
	}
	if out == "" {
		if ansi.StringWidth(last) > w {
			return ""
		}
		return last
	}
	return out + " · " + last
}

// inputLabel is the footer prompt's label, styled.
func (m *Model) inputLabel() string {
	switch m.mode {
	case modeInputDir:
		if m.wtFlow {
			return stHeader.Render("repo:")
		}
		// The new session lands in the selected row's group — silently, unless
		// it is said here, where the choice is still one esc away.
		if g := m.st.Group(m.contextGroup()); g != nil {
			return stHeader.Render("dir → ") +
				lipgloss.NewStyle().Foreground(groupLip(g.Color)).Render(truncRunes(g.Name, 14)) +
				stHeader.Render(":")
		}
		return stHeader.Render("dir:")
	case modeInputWtName:
		return stHeader.Render("worktree:")
	case modeInputGroup:
		return stHeader.Render("group:")
	case modeRename:
		return stHeader.Render("name:")
	}
	return stHeader.Render(">")
}

// ---- list ------------------------------------------------------------

func (m *Model) viewList() string {
	iw := m.width
	ih := m.listInnerHeight()
	lines := make([]string, 0, ih)

	if len(m.rows) == 0 {
		empty := []string{
			"",
			stDim.Render("  nothing on the desk yet"),
			"",
			"  " + stHeader.Render("n") + stText.Render(" start a new session"),
			"  " + stHeader.Render("i") + stText.Render(" add a past conversation"),
		}
		if m.filter != "" {
			empty = []string{"", stDim.Render("  no match for “" + m.filter + "”")}
		}
		lines = append(lines, empty...)
	}

	if m.top >= len(m.rows) {
		m.top = 0
	}
	end := m.top + ih
	if end > len(m.rows) {
		end = len(m.rows)
	}

	// Number badges count OPEN sessions from the very top, not the scroll
	// window, so a digit always means the same thing as the n-th tab.
	numFor := map[int]int{}
	sessionIndex := 0
	for i, r := range m.rows {
		if r.kind == rowSession && m.isLive(r.id) {
			sessionIndex++
			if sessionIndex <= 9 {
				numFor[i] = sessionIndex
			}
		}
	}

	for i := m.top; i < end; i++ {
		r := m.rows[i]
		var line string
		if r.kind == rowGroup {
			line = m.renderGroupRow(r.id, iw)
		} else {
			line = m.renderSessionRow(r.id, numFor[i], iw)
		}
		if i == m.sel {
			line = stSelected.Render(pad(line, iw))
		}
		lines = append(lines, line)
	}
	for len(lines) < ih {
		lines = append(lines, "")
	}
	lines = lines[:ih]
	for i := range lines {
		lines[i] = pad(lines[i], iw)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderGroupRow(gid string, w int) string {
	if gid == oldSection {
		arrow := "▸"
		if m.st.OldExpanded {
			arrow = "▾"
		}
		n := len(m.archivedIDs())
		return stFaint.Render(" "+arrow+" ") + stDormant.Render("old") + stFaint.Render(fmt.Sprintf(" %d", n))
	}
	g := m.st.Group(gid)
	if g == nil {
		return ""
	}
	gc := lipgloss.NewStyle().Foreground(groupLip(g.Color))
	arrow := "▾"
	if g.Collapsed {
		arrow = "▸"
	}
	members := m.membersOf(gid)
	suffix := stDim.Render(fmt.Sprintf(" %d", len(members)))
	// Surface hidden alerts on collapsed groups so nothing gets buried.
	if g.Collapsed {
		alerts := 0
		for _, sid := range members {
			if k := m.statusOf(sid); k == status.NeedsYou || k == status.Attention {
				alerts++
			}
		}
		if alerts > 0 {
			suffix += stAlert.Render(fmt.Sprintf(" ◆%d", alerts))
		}
	}
	return gc.Render(" "+arrow+" ") + gc.Bold(true).Render(g.Name) + suffix
}

func (m *Model) renderSessionRow(id string, num, w int) string {
	s := m.st.Session(id)
	if s == nil {
		return ""
	}
	k := m.statusOf(id)
	icon, ist := m.statusGlyph(k)

	// active-in-viewport marker
	activeMark := " "
	if id == m.activeID {
		activeMark = stActive.Render("▎")
	}
	numStr := "  "
	if num > 0 {
		numStr = stDim.Render(fmt.Sprintf("%d ", num))
	}
	indent := ""
	if s.Archived {
		indent = stFaint.Render("▏") + " "
	} else if g := m.st.Group(s.GroupID); g != nil {
		// colored rail ties member rows to their group, Chrome-style
		indent = lipgloss.NewStyle().Foreground(groupLip(g.Color)).Render("▏") + " "
	}

	nameStyle := stText
	switch k {
	case status.NeedsYou:
		nameStyle = stAlert
	case status.Attention:
		nameStyle = stNew
	case status.Dormant:
		nameStyle = stDormant
	}

	// The right-hand side is a set of fixed-width columns. Each one holds its
	// width even when it has no value, so rows line up across agents (Codex
	// reports no cost) and across a session's life (no model until the first
	// turn). Numbers are right-aligned so magnitudes compare down the column.
	var cols []string
	if m.width >= 46 {
		cell := blank(costW)
		if c := m.costOf(id); c >= 0.01 {
			cell = stFaint.Render(padNum(fmtUSD(c), costW))
		}
		cols = append(cols, cell)
	}
	family := m.familyOf(id)
	if m.width >= 40 {
		cell := blank(modelW)
		if family != "" {
			cell = familyStyle(family).Render(pad(family, modelW))
		}
		cols = append(cols, cell)
	}
	if m.width >= 30 {
		cell := blank(tokenW)
		if tok := m.tokensOf(id); tok > 0 {
			cell = tokenStyle(tok, m.contextWindowOf(id)).Render(padNum(fmtTokens(tok), tokenW))
		}
		cols = append(cols, cell)
	}

	var age string
	switch k {
	case status.NeedsYou:
		age = stAlert.Render(padNum("!", ageW))
	case status.Attention:
		age = stNew.Render(padNum("new", ageW))
	case status.Working:
		age = stWorking.Render(padNum(ago(m.runtime[id].UpdatedAt), ageW))
	case status.Dormant:
		age = stDormant.Render(padNum("zz", ageW))
	default:
		age = stDim.Render(padNum(ago(m.runtime[id].UpdatedAt), ageW))
	}
	cols = append(cols, age)
	right := strings.Join(cols, " ")

	left := activeMark + indent + numStr + ist.Render(icon) + " "
	avail := w - ansi.StringWidth(left) - ansi.StringWidth(right) - 1
	name := nameStyle.Render(pad(s.Name, avail))
	return left + name + " " + right
}
func familyStyle(family string) lipgloss.Style {
	fg := lipgloss.Color("246")
	switch {
	case family == "fable":
		fg = lipgloss.Color("218")
	case family == "opus":
		fg = lipgloss.Color("183")
	case family == "sonnet":
		fg = lipgloss.Color("117")
	case family == "haiku":
		fg = lipgloss.Color("114")
	case strings.HasPrefix(family, "gpt"), strings.HasPrefix(family, "codex"):
		fg = lipgloss.Color("79") // Codex models: teal, distinct from Claude
	}
	return lipgloss.NewStyle().Foreground(fg)
}

// agentStyle tints an agent label so mixed desks are readable at a glance.
func agentStyle(agent string) lipgloss.Style {
	if agent == state.AgentCodex {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("79"))
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
}

// modelWindow is the context window assumed for coloring token usage.
func modelWindow(family string) int {
	switch family {
	case "fable", "opus", "sonnet":
		return 1_000_000 // these tiers run 1M in this org
	default:
		return 200_000
	}
}

// fmtUSD renders an estimated dollar amount compactly ("$0.42", "$3.7",
// "$128").
func fmtUSD(v float64) string {
	switch {
	case v >= 100:
		return fmt.Sprintf("$%.0f", v)
	case v >= 10:
		return fmt.Sprintf("$%.1f", v)
	default:
		return fmt.Sprintf("$%.2f", v)
	}
}

// fmtTokens renders a token count in at most four cells ("87k", "1.2M",
// "13M"), the width of the token column — a wider string would be truncated
// and lose its unit.
func fmtTokens(n int) string {
	k := (n + 500) / 1000
	if k < 1 {
		k = 1
	}
	switch {
	case k < 1000:
		return fmt.Sprintf("%dk", k) // up to "999k"
	case k < 10000:
		m := float64(k) / 1000
		if k%1000 == 0 {
			return fmt.Sprintf("%dM", k/1000) // "1M"
		}
		return fmt.Sprintf("%.1fM", m) // "1.2M"
	default:
		return fmt.Sprintf("%dM", (k+500)/1000) // "13M"
	}
}

// tokenStyle colors the context size by how full the model's window is.
func tokenStyle(n, window int) lipgloss.Style {
	switch {
	case n*100 >= window*85:
		return stAlert
	case n*100 >= window*60:
		return stNew
	default:
		return stFaint
	}
}

// ---- import picker -----------------------------------------------------

func (m *Model) viewImport() string {
	iw := m.width
	ih := m.listInnerHeight()
	lines := []string{
		" " + stHeader.Render("add a past conversation"),
		" " + stHeader.Render("⌕") + m.search.View(),
		stFaint.Render(strings.Repeat("┄", iw)),
	}
	filtered := m.importFiltered()
	rows := ih - len(lines)
	perItem := 2
	visible := rows / perItem
	if visible < 1 {
		visible = 1
	}
	if m.importSel < m.importTop {
		m.importTop = m.importSel
	}
	if m.importSel >= m.importTop+visible {
		m.importTop = m.importSel - visible + 1
	}
	switch {
	case m.scanning:
		lines = append(lines, "", stDim.Render("  scanning ~/.claude/projects "+spinnerFrames[m.spin%len(spinnerFrames)]))
	case len(filtered) == 0:
		lines = append(lines, "", stDim.Render("  nothing found"))
	default:
		endI := m.importTop + visible
		if endI > len(filtered) {
			endI = len(filtered)
		}
		for i := m.importTop; i < endI; i++ {
			it := m.importItems[filtered[i]]
			title := " " + it.Title
			meta := "   " + agentStyle(it.Agent).Render(it.Agent) +
				stDim.Render(" · "+shortDir(it.Dir)+" · "+ago(it.MTime))
			if i == m.importSel {
				lines = append(lines,
					stSelected.Render(pad(stText.Bold(true).Render(title), iw)),
					stSelected.Render(pad(meta, iw)))
			} else {
				lines = append(lines, pad(stText.Render(title), iw), pad(meta, iw))
			}
		}
		if len(filtered) > visible {
			lines = append(lines, stDim.Render(fmt.Sprintf("  %d/%d", m.importSel+1, len(filtered))))
		}
	}
	for len(lines) < ih {
		lines = append(lines, "")
	}
	lines = lines[:ih]
	for i := range lines {
		lines[i] = pad(lines[i], iw)
	}
	return strings.Join(lines, "\n")
}

func osUserHome() (string, error) { return os.UserHomeDir() }

func shortDir(dir string) string {
	if home, err := osUserHome(); err == nil && strings.HasPrefix(dir, home) {
		return "~" + dir[len(home):]
	}
	return dir
}

// ---- overlays -------------------------------------------------------

// viewConfirm renders a question in the middle of the screen. A question in the
// footer is missed: you press a key expecting it to act, and the answer is one
// line away from where you are looking — which for "delete forever" is the wrong
// place to be subtle.
func (m *Model) viewConfirm() string {
	q, hint := m.confirmMsg, "y confirm · n cancel"
	// The message may carry its own "(y/n)"; the box says that already.
	for _, suffix := range []string{" (y/n)", "(y/n)"} {
		q = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(q), suffix))
	}
	inner := max(ansi.StringWidth(q), ansi.StringWidth(hint))
	if maxW := m.width - 8; inner > maxW && maxW > 10 {
		inner = maxW
		q = ansi.Truncate(q, inner, "…")
	}
	lines := []string{
		pad(stText.Bold(true).Render(ansi.Truncate(q, inner, "…")), inner),
		pad("", inner),
		pad(stDim.Render(hint), inner),
	}
	return stOverlay.Render(strings.Join(lines, "\n"))
}

func (m *Model) viewGroupPick() string {
	title := "move to group"
	switch m.pickKind {
	case "sort":
		title = "sort sessions by"
	case "color":
		title = "group color"
	case "agent":
		title = "agent"
	case "menu":
		if m.menuRow.kind == rowGroup {
			if g := m.st.Group(m.menuRow.id); g != nil {
				title = g.Name
			} else {
				title = "old"
			}
		} else if s := m.st.Session(m.menuRow.id); s != nil {
			title = ansi.Truncate(s.Name, 24, "…")
		}
	}
	// Widest label decides the column the shortcuts line up in.
	labelW := 0
	for _, it := range m.pickItems {
		if w := ansi.StringWidth(it.label); w > labelW {
			labelW = w
		}
	}
	var b strings.Builder
	b.WriteString(stHeader.Render(title) + "\n\n")
	for i, it := range m.pickItems {
		label := it.label
		if it.key != "" {
			label = pad(label, labelW) + "  " + stDim.Render(it.key)
		}
		if m.pickKind == "color" {
			// render each option as a swatch in its own color
			var slot int
			fmt.Sscanf(it.id, "%d", &slot)
			label = lipgloss.NewStyle().Foreground(groupLip(slot)).Render("■ " + it.label)
		} else if i != m.pickSel {
			label = stDim.Render(label)
		}
		if i == m.pickSel {
			b.WriteString(stSelected.Render(stText.Render("▸ ")+label+" ") + "\n")
		} else {
			b.WriteString("  " + label + " \n")
		}
	}
	b.WriteString("\n" + stDim.Render("enter choose · esc cancel"))
	return stOverlay.Render(b.String())
}

// viewInfo is the per-session details popup (v / right-click → info).
func (m *Model) viewInfo() string {
	s := m.st.Session(m.infoTarget)
	if s == nil {
		return ""
	}
	vw := m.width - 4
	if vw > 56 {
		vw = 56
	}
	if vw < 16 {
		vw = 16
	}
	info := m.probeInfo(s.ID)
	k := m.statusOf(s.ID)
	icon, ist := m.statusGlyph(k)

	// A value wider than the box wraps onto indented continuation lines —
	// the dir, the conversation id and the tmux name are the values this
	// popup exists for, and a narrow sidebar must not eat them.
	row := func(label string, val string) string {
		avail := vw - 9
		if ansi.StringWidth(val) <= avail {
			return stDim.Render(pad(label, 9)) + val
		}
		wrapped := strings.Split(ansi.Wrap(val, avail, ""), "\n")
		if len(wrapped) > 3 {
			wrapped = wrapped[:3]
			wrapped[2] = ansi.Truncate(wrapped[2], avail-1, "…")
		}
		out := stDim.Render(pad(label, 9)) + wrapped[0]
		for _, l := range wrapped[1:] {
			out += "\n" + blank(9) + l
		}
		return out
	}
	var lines []string
	lines = append(lines, stText.Bold(true).Render(ansi.Truncate(s.Name, vw, "…")), "")

	statusVal := ist.Render(icon) + " " + string(k)
	if r := m.runtime[s.ID]; r.Message != "" {
		statusVal += stDim.Render(" — ") + stNew.Render(r.Message)
	} else if !m.runtime[s.ID].UpdatedAt.IsZero() {
		statusVal += stDim.Render(" · " + agoLong(m.runtime[s.ID].UpdatedAt))
	}
	lines = append(lines, row("status", statusVal))

	lines = append(lines, row("agent", agentStyle(s.AgentOf()).Render(s.AgentOf())))
	family := m.familyOf(s.ID)
	if family != "" {
		v := familyStyle(family).Render(family)
		if info.Model != "" {
			v += stDim.Render(" · " + info.Model)
		}
		lines = append(lines, row("model", v))
	}
	if info.ContextTokens > 0 {
		window := m.contextWindowOf(s.ID)
		pct := info.ContextTokens * 100 / window
		lines = append(lines, row("context",
			tokenStyle(info.ContextTokens, window).Render(fmtTokens(info.ContextTokens))+
				stDim.Render(fmt.Sprintf(" · ~%d%% of %s · size right now", pct, fmtTokens(window)))))
	}
	if c := m.costOf(s.ID); c >= 0.01 {
		// Say which window this covers. Claude's own /usage reports only the
		// current process, so a whole-conversation figure looks wrong beside it
		// unless it is labelled.
		lines = append(lines, row("cost",
			stText.Render(fmtUSD(c))+stDim.Render(" · whole conversation, every resume, incl. subagents")))
	}
	if info.TodosTotal > 0 {
		v := fmt.Sprintf("%d/%d done", info.TodosDone, info.TodosTotal)
		if info.CurrentTodo != "" {
			v += " · ▸ " + info.CurrentTodo
		}
		lines = append(lines, row("todos", stText.Render(v)))
	}
	lines = append(lines, row("dir", stText.Render(shortDir(s.Dir))))
	if m.infoGit != "" {
		lines = append(lines, row("git", stText.Render(m.infoGit)))
	}
	if g := m.st.Group(s.GroupID); g != nil {
		lines = append(lines, row("group",
			lipgloss.NewStyle().Foreground(groupLip(g.Color)).Render("■ "+g.Name)))
	}
	if !s.LastOpenedAt.IsZero() {
		lines = append(lines, row("opened", stText.Render(agoLong(s.LastOpenedAt))))
	}
	// the conversation's true age, not agentboss's bookkeeping
	if born := info.Born; !born.IsZero() {
		lines = append(lines, row("started", stText.Render(agoLong(born))))
	} else if !s.CreatedAt.IsZero() {
		lines = append(lines, row("started", stText.Render(agoLong(s.CreatedAt))))
	}
	if s.SessionID != "" {
		lines = append(lines, row("conv id", stDim.Render(s.SessionID)))
	}
	live := "dormant"
	if m.isLive(s.ID) {
		live = "live · tmux " + state.TmuxName(s.ID)
	}
	lines = append(lines, row("process", stDim.Render(live)))
	lines = append(lines, "", stDim.Render("any key closes"))

	// A wrapped row is several screen lines; flatten before padding.
	var flat []string
	for _, l := range lines {
		flat = append(flat, strings.Split(l, "\n")...)
	}
	for i := range flat {
		flat[i] = pad(flat[i], vw)
	}
	return stOverlay.Render(strings.Join(flat, "\n"))
}

// viewHelp is a full-body page (not a floating box) so it always fits the
// sidebar, however narrow.
func (m *Model) viewHelp() string {
	iw := m.width
	ih := m.listInnerHeight()
	k := modKey
	rows := [][2]string{
		{"enter", "open (wakes dormant)"},
		{k("o"), "open, keep focus here"},
		{"ctrl+\\", "sidebar ⇄ session"},
		{"tab", "focus the session"},
		{k("[") + " " + k("]"), "prev / next session"},
		{k("1") + "-" + k("9"), "n-th open session"},
		{k("a"), "next session needing you"},
		{k("/"), "search"},
		{k("n"), "new session"},
		{k("W"), "new session in a git worktree"},
		{k("i"), "import past conversation"},
		{k("N") + " " + k("r") + " " + k("m"), "group / rename / regroup"},
		{"", "(also renames in the agent)"},
		{k("J") + " " + k("K"), "reorder (or drag)"},
		{k("s"), "sort: status/recent/name/dir"},
		{k("v"), "session info popup"},
		{k("f") + " " + k("F"), "its folder · scratchpad"},
		{k("M"), "mute / unmute alerts"},
		{"right-click", "context menu (rows & tabs)"},
		{k("c"), "cycle group color"},
		{k("spc"), "collapse group (tab folds too)"},
		{k("<") + " " + k(">"), "sidebar width"},
		{k("z"), "close tab, stays on desk"},
		{k("x"), "close to old · in old: delete"},
		{k("u"), "reopen what you just closed"},
		{k("q"), "quit, sessions live on"},
	}
	// The key column is exactly as wide as its widest label.
	keyW := 0
	for _, r := range rows {
		if w := ansi.StringWidth(r[0]); w > keyW {
			keyW = w
		}
	}
	keyW += 2
	lines := []string{" " + stHeader.Render("keys"), ""}
	for _, r := range rows {
		// A narrow sidebar wraps the description instead of chopping it — the
		// help page is where chopped words cost the most.
		avail := max(8, iw-keyW-2)
		parts := strings.Split(ansi.Wordwrap(r[1], avail, " "), "\n")
		lines = append(lines, " "+stText.Bold(true).Render(pad(r[0], keyW))+stDim.Render(parts[0]))
		for _, p := range parts[1:] {
			lines = append(lines, " "+blank(keyW)+stDim.Render(p))
		}
	}
	lines = append(lines, "",
		" "+stWorking.Render("⠙ working ")+stAlert.Render("◆ needs you"),
		" "+stNew.Render("● new ")+stIdle.Render("· idle ")+stDormant.Render("○ dormant"),
		"",
		" "+stDim.Render("every chord works from inside an agent too;"),
		" "+stDim.Render("bare letters never act — stray typing is safe"),
		" "+stDim.Render("tmux prefix here: ctrl+q · any key closes"))
	for len(lines) < ih {
		lines = append(lines, "")
	}
	lines = lines[:ih]
	for i := range lines {
		lines[i] = pad(lines[i], iw)
	}
	return strings.Join(lines, "\n")
}
