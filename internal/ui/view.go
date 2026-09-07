package ui

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/tallu-wonder/agentboss/internal/keymap"
	"github.com/tallu-wonder/agentboss/internal/state"
	"github.com/tallu-wonder/agentboss/internal/status"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// optName is what this platform calls the Alt modifier — the key legend must
// name the key on the user's keyboard, not tmux's name for it.
func optName() string {
	if strings.HasPrefix(keymap.Display("n"), "⌥") {
		return "opt"
	}
	return "alt"
}

func modKey(k string) string { return keymap.Display(k) }

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
	cWorking = lipgloss.Color("114") // green: running
	cAlert   = lipgloss.Color("203") // red-ish: needs you
	cNew     = lipgloss.Color("221") // yellow: finished, unseen
	cDim     = lipgloss.Color("247")
	cFaint   = lipgloss.Color("244")
	cText    = lipgloss.Color("252")
	cSelBg   = lipgloss.Color("237")

	stHeader   = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	stWorking  = lipgloss.NewStyle().Foreground(cWorking)
	stAlert    = lipgloss.NewStyle().Foreground(cAlert).Bold(true)
	stNew      = lipgloss.NewStyle().Foreground(cNew)
	stIdle     = lipgloss.NewStyle().Foreground(lipgloss.Color("215")) // amber: paused
	stStopped  = lipgloss.NewStyle().Foreground(cAlert)
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
func (m *Model) listTopY() int { return 3 }

func (m *Model) listInnerHeight() int { return max(0, m.height-4) } // header, rule, footer

// ---- helpers ---------------------------------------------------------

// Widths of the compact metric columns, measured in terminal cells.
const (
	costW    = 5
	modelW   = 7
	tokenW   = 4
	ageW     = 3
	contextW = 4
	projectW = 18
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
		return "▶", stWorking
	case status.NeedsYou:
		return "◆", stAlert
	case status.Attention:
		return "●", stNew
	case status.Idle:
		return "⏸", stIdle
	default:
		return "■", stStopped
	}
}

// ---- top-level view --------------------------------------------------

func (m *Model) View() string {
	if m.width < 16 || m.height < 10 {
		return "Resize pane: at least 16 columns × 10 rows"
	}
	var body string
	switch m.mode {
	case modeHelp:
		body = m.overlay(m.viewHelp())
	case modeInfo:
		body = m.overlay(m.viewInfo())
	case modeGroupPick:
		body = m.overlay(m.viewGroupPick())
	case modeConfirm:
		body = m.overlay(m.viewConfirm())
	case modeImport:
		body = m.viewImport()
	case modeCommands:
		body = m.viewCommands()
	case modeInputDir, modeInputWtName, modeInputGroup, modeRename:
		body = m.overlay(m.viewInput())
	default:
		body = m.viewList()
	}
	return m.viewHeader() + "\n" +
		m.viewFocus() + "\n" + m.viewFilterBar() + "\n" +
		body + "\n" +
		m.viewFooter()
}

// overlay floats a box over the session list rather than replacing it — a
// confirmation for "remove from desk?" must not hide the very row it is about.
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
	left := " " + stHeader.Render("agentboss")
	if working > 0 {
		left += stWorking.Render(fmt.Sprintf(" ⠙%d", working))
	}
	if needs > 0 {
		left += stAlert.Render(fmt.Sprintf(" ◆%d", needs))
	}
	if attn > 0 {
		left += stNew.Render(fmt.Sprintf(" ●%d", attn))
	}
	if m.st.NotifyMuted {
		left += stDim.Render(" muted")
	}
	right := stDim.Render(modKey("p") + " commands ")
	if ansi.StringWidth(left+right)+1 > m.width {
		right = stDim.Render(modKey("p") + " ")
	}
	return pad(left, max(0, m.width-ansi.StringWidth(right))) + right
}

func (m *Model) viewFooter() string {
	var text string
	switch {
	case m.mode == modeSearch:
		shown, total := m.matchCount()
		text = " /" + m.search.View() + fmt.Sprintf(" %d/%d", shown, total)
	case m.inputMode():
		text = " " + stDim.Render("Enter next · Esc cancel")
	case m.mode == modeHelp || m.mode == modeInfo:
		text = " " + stDim.Render("↑↓ / PgUp PgDn scroll · Esc close")
	case m.mode == modeConfirm || m.mode == modeGroupPick || m.mode == modeCommands:
		text = ""
	case m.mode == modeImport:
		text = " " + stDim.Render("Enter add · Esc cancel")
	case m.notice != "":
		style := stNotice
		if m.noticeErr {
			style = stErr
		}
		text = " " + style.Render(m.notice)
	case m.unfocused:
		text = " " + stDim.Render(fitHints(m.width-2, "Ctrl+\\ sidebar", modKey("p")+" commands"))
	default:
		text = " " + stDim.Render(fitHints(m.width-2, m.footerHints()...))
	}
	return pad(text, m.width)
}

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
	if len(m.marked) > 0 {
		return []string{fmt.Sprintf("%d selected", len(m.marked)), modKey("m") + " move", modKey("x") + " archive", "Esc clear"}
	}
	r := m.selectedRow()
	if r != nil && r.kind == rowGroup {
		return []string{"↵ fold", modKey("r") + " rename", modKey("p") + " commands"}
	}
	return []string{"↵ open", modKey("n") + " new", modKey("b") + " select", modKey("p") + " commands"}
}

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

func (m *Model) viewList() string {
	h := m.listRowsHeight()
	var lines []string
	if len(m.rows) == 0 {
		if m.filtersActive() {
			text := "No sessions match these filters"
			if m.attentionOnly {
				text = "Nothing needs attention"
			}
			lines = []string{"", stDim.Render("  " + text), "", "  Esc clears filters"}
		} else {
			lines = []string{"", stText.Render("  Nothing on the desk yet"), "", "  " + keymap.Hint("n", "start a new session"), "  " + keymap.Hint("i", "import a conversation"), "  " + keymap.Hint("p", "browse commands")}
		}
	}
	nums := map[string]int{}
	for n, id := range m.numberedLive() {
		if n < 9 {
			nums[id] = n + 1
		}
	}
	if m.top >= len(m.rows) {
		m.top = 0
	}
	for i := m.top; i < len(m.rows); i++ {
		r := m.rows[i]
		if len(lines)+m.rowHeight(r) > h {
			break
		}
		line := ""
		if r.kind == rowGroup {
			line = m.renderGroupRow(r.id, m.width)
		} else {
			line = m.renderSessionRow(r.id, nums[r.id], m.width)
		}
		if i == m.sel {
			line = stSelected.Render(pad(line, m.width))
		}
		lines = append(lines, line)
		if m.rowHeight(r) > 1 {
			lines = append(lines, m.attentionReason(r.id))
		}
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	lines = lines[:h]
	if m.height >= 16 {
		lines = append(lines, m.selectedDetails()...)
	}
	return fitLines(lines, m.width, m.listInnerHeight())
}

func (m *Model) renderGroupRow(gid string, w int) string {
	if gid == oldSection {
		arrow := "▸"
		if m.st.OldExpanded {
			arrow = "▾"
		}
		n := len(m.archivedIDs())
		return stFaint.Render(" "+arrow+" ") + stDormant.Render("Archived") + stFaint.Render(fmt.Sprintf(" %d", n))
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
	icon, style := m.statusGlyph(k)
	cursor, active, mark := " ", " ", ""
	if id == m.selectedSessionID() {
		cursor = "›"
	}
	if id == m.activeID {
		active = stActive.Render("▎")
	}
	if len(m.marked) > 0 {
		mark = "· "
		if m.marked[id] {
			mark = stNotice.Render("✓ ")
		}
	}
	number := "  "
	if num > 0 {
		number = stDim.Render(fmt.Sprintf("%d ", num))
	}
	left := cursor + active + mark + number
	if g := m.st.Group(s.GroupID); g != nil {
		left = cursor + active + lipgloss.NewStyle().Foreground(groupLip(g.Color)).Render("│") + mark + number
	}
	// Leave two cells between the status icon and the pane divider.
	right := style.Render(icon) + "  "
	if m.st.ShowMetrics && w >= 46 {
		// Reserve the same cells on every row, including missing/unpriced
		// values, so numbers stay aligned across agents and status changes.
		model := familyStyle(m.familyOf(id)).Render(pad(m.familyOf(id), modelW))
		context := tokenStyle(m.tokensOf(id), m.contextWindowOf(id)).Render(padNum(m.contextValue(id), contextW))
		cost := blank(costW)
		if value := m.costOf(id); value >= .01 {
			cost = stText.Render(padNum(fmtUSD(value), costW))
		}
		right = model + " " + context + " " + cost + " " + right
	}
	if w >= 90 {
		right = stDim.Render(pad(shortProject(s.Dir), projectW)) + " · " + right
	}
	avail := w - ansi.StringWidth(left+right) - 1
	nameStyle := stText
	if k == status.NeedsYou || k == status.Attention {
		nameStyle = style
	}
	if k == status.Dormant {
		nameStyle = stDormant
	}
	return left + nameStyle.Render(pad(s.Name, max(1, avail))) + " " + right
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

// fmtUSD rounds an estimated cost to whole dollars, retaining compact k/M
// notation for large totals. The underlying estimate keeps its precision.
func fmtUSD(v float64) string {
	v = math.Round(v)
	if v >= 10_000 {
		// The same compact k/M notation as token counts keeps large totals
		// within five cells, including the dollar sign.
		return "$" + fmtTokens(int(v))
	}
	return fmt.Sprintf("$%.0f", v)
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
		lines = append(lines, "", stDim.Render("  scanning agent conversations "+spinnerFrames[m.spin%len(spinnerFrames)]))
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
// line away from where you are looking — which for "remove from desk" is the wrong
// place to be subtle.
func (m *Model) viewConfirm() string {
	lines := strings.Split(m.confirmMsg, "\n")
	if len(lines) > 0 {
		lines[0] = stText.Bold(true).Render(lines[0])
	}
	return m.scrollBox(lines, "y confirm · n / Esc cancel")
}

func (m *Model) viewGroupPick() string {
	w := max(8, m.width-6)
	lines := []string{stHeader.Render(pad(m.pickerTitle(), w)), ""}
	start, end := m.pickerWindow()
	for i := start; i < end; i++ {
		it := m.pickItems[i]
		label := it.label
		if it.key != "" {
			label = pad(label, max(1, w-ansi.StringWidth(it.key)-4)) + " " + it.key
		}
		if m.pickKind == "color" {
			var slot int
			fmt.Sscanf(it.id, "%d", &slot)
			label = lipgloss.NewStyle().Foreground(groupLip(slot)).Render("■ " + label)
		}
		if i == m.pickSel {
			lines = append(lines, stSelected.Render(pad("› "+label, w)))
		} else {
			lines = append(lines, pad("  "+label, w))
		}
	}
	if m.inputErr != "" {
		lines = append(lines, stErr.Render(ansi.Wrap(m.inputErr, w, "")))
	} else if m.pickKind == "agent" {
		lines = append(lines, stDim.Render(pad(shortDir(m.pendingDir), w)))
	} else {
		lines = append(lines, "")
	}
	if len(m.pickItems) > m.pickerVisible() {
		lines = append(lines, stDim.Render(fmt.Sprintf("%d/%d", m.pickSel+1, len(m.pickItems))))
	}
	lines = append(lines, stDim.Render(pad(m.pickerHint(), w)))
	// Leave the list and footer anchored; detailed errors may be scrolled.
	if len(strings.Split(strings.Join(lines, "\n"), "\n"))+2 > m.listInnerHeight() {
		return m.scrollBox(lines[:len(lines)-1], m.pickerHint())
	}
	for i := range lines {
		lines[i] = pad(lines[i], w)
	}
	return stOverlay.Render(strings.Join(lines, "\n"))
}

func (m *Model) viewInfo() string {
	s := m.st.Session(m.infoTarget)
	if s == nil {
		return ""
	}
	vw := m.width - 6
	if vw > 56 {
		vw = 56
	}
	if vw < 8 {
		vw = 8
	}
	info := m.probeInfo(s.ID)
	k := m.statusOf(s.ID)
	icon, ist := m.statusGlyph(k)

	// A value wider than the box wraps onto indented continuation lines —
	// the dir, the conversation id and the tmux name are the values this
	// popup exists for, and a narrow sidebar must not eat them.
	row := func(label string, val string) string {
		avail := max(1, vw-9)
		if ansi.StringWidth(val) <= avail {
			return stDim.Render(pad(label, 9)) + val
		}
		wrapped := strings.Split(ansi.Wrap(val, avail, ""), "\n")

		out := stDim.Render(pad(label, 9)) + wrapped[0]
		for _, l := range wrapped[1:] {
			out += "\n" + blank(9) + l
		}
		return out
	}
	var lines []string
	lines = append(lines, stText.Bold(true).Render(ansi.Truncate(s.Name, vw, "…")), "")

	statusText := strings.ReplaceAll(string(k), "_", " ")
	if k == status.Dormant {
		statusText = "stopped · resumable"
	}
	statusVal := ist.Render(icon) + " " + statusText
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
		description := "size right now"
		if s.AgentOf() == state.AgentClaude {
			description = "estimated current size"
		}
		lines = append(lines, row("context",
			tokenStyle(info.ContextTokens, window).Render(fmtTokens(info.ContextTokens))+
				stDim.Render(fmt.Sprintf(" · %d%% of %s · %s", pct, fmtTokens(window), description))))
	}
	if c := m.costOf(s.ID); c >= 0.01 {
		// Say which window this covers. Claude's own /usage reports only the
		// current process, so a whole-conversation figure looks wrong beside it
		// unless it is labelled.
		lines = append(lines, row("est cost",
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

	// A wrapped row is several screen lines; flatten before padding.
	var flat []string
	for _, l := range lines {
		flat = append(flat, strings.Split(l, "\n")...)
	}
	for i := range flat {
		flat[i] = pad(flat[i], vw)
	}
	return m.scrollBox(flat, "↑↓ scroll · Esc close")
}

// viewHelp is a full-body page (not a floating box) so it always fits the
// sidebar, however narrow.
func (m *Model) viewHelp() string {
	return m.scrollBox(m.helpLines(), "↑↓ / PgUp PgDn scroll · Esc close")
}
