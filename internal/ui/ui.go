// Package ui is the agentdeck sidebar: a Bubble Tea app running in the left
// pane of the agentdeck tmux window. It lists every session with live status,
// drives the viewport pane (a nested tmux client showing the active
// session), and renders the tab bar into the outer session's status line.
package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/tallu-wonder/agentdeck/internal/agents"
	"github.com/tallu-wonder/agentdeck/internal/notify"
	"github.com/tallu-wonder/agentdeck/internal/paths"
	"github.com/tallu-wonder/agentdeck/internal/reveal"
	"github.com/tallu-wonder/agentdeck/internal/sanitize"
	"github.com/tallu-wonder/agentdeck/internal/state"
	"github.com/tallu-wonder/agentdeck/internal/status"
	"github.com/tallu-wonder/agentdeck/internal/tmuxctl"
)

// Status aliases used across the package.
const (
	alertNeedsYou  = status.NeedsYou
	alertAttention = status.Attention
)

type mode int

const (
	modeNormal mode = iota
	modeSearch
	modeInputDir   // quick create: directory (name = folder name)
	modeInputGroup // typing a new group's name
	modeRename     // renaming a session or group
	modeGroupPick  // choose a group to move a session into
	modeConfirm    // y/n confirmation
	modeImport     // pick a past conversation to add
	modeHelp
	modeInfo // per-session details popup
)

type tickMsg time.Time

type importScanMsg struct{ items []agents.Conversation }

type pickItem struct {
	id    string // group ID; special values: "" = no group, "+new" = create
	label string
	key   string // single-key shortcut, context menus only
}

// Model is the Bubble Tea model for the sidebar.
type Model struct {
	st        *state.State
	statePath string
	claudeBin string
	selfBin   string
	firstRun  bool

	width, height int

	rows []row
	sel  int
	top  int // list scroll offset

	live    map[string]tmuxctl.Info
	runtime map[string]status.Runtime

	// viewport wiring
	sidebarPane string
	vpPane      string
	vpTTY       string
	activeID    string // session shown in the viewport ("" = placeholder)
	tabCache    string
	tabScroll   int // first visible tab when the strip overflows
	lastTotalW  int // window width at last tick, to tell resizes from drags

	// auto-naming: sessions the user hasn't renamed track the Claude
	// conversation's own title
	nameProbes map[string]*nameProbe
	// liveModels: model family read from each live session's own status bar
	// — updates the instant /model is switched, unlike the transcript
	liveModels map[string]string

	// notified remembers the alert each session was last announced for, so one
	// alert produces one notification rather than one per tick. Cleared when
	// the session goes quiet, so the next alert notifies again.
	notified    map[string]status.Kind
	notifyReady bool // seeded: alerts already present at startup don't fire

	mode   mode
	input  textinput.Model
	search textinput.Model
	filter string

	// unfocused: the keyboard is elsewhere (usually inside the agent). Read
	// from tmux's pane_active each tick; the sidebar dims its header so a
	// glance answers "where will my keys land?".
	unfocused bool

	// pendingDir carries the folder from the new-session prompt to the
	// agent picker.
	pendingDir string

	// picker overlay: "group" | "sort" | "menu" | "color" | "agent"
	pickKind  string
	pickFor   string
	pickSel   int
	pickItems []pickItem

	// mouse drag & drop
	dragID      string // session being dragged
	dragGroupID string // group header being dragged
	dragMoved   bool
	pressIdx    int

	menuRow row // target of the right-click context menu

	// info popup
	infoTarget string
	infoGit    string

	// import picker
	importItems []agents.Conversation
	importSel   int
	importTop   int
	scanning    bool

	// confirm
	confirmMsg string
	confirmFn  func() tea.Cmd

	renameTarget row

	spin int

	notice      string
	noticeErr   bool
	noticeUntil time.Time

	dirty bool // state changed, save on next tick
}

// New builds the sidebar model.
func New(st *state.State, statePath, claudeBin, selfBin string, firstRun bool) *Model {
	in := textinput.New()
	in.CharLimit = 512
	se := textinput.New()
	se.Placeholder = "filter…"
	se.CharLimit = 128
	m := &Model{
		st:         st,
		statePath:  statePath,
		claudeBin:  claudeBin,
		selfBin:    selfBin,
		firstRun:   firstRun,
		input:      in,
		search:     se,
		live:       map[string]tmuxctl.Info{},
		runtime:    map[string]status.Runtime{},
		nameProbes: map[string]*nameProbe{},
		liveModels: map[string]string{},
	}
	m.ensureLayout()
	m.refresh()
	m.syncNames()
	m.buildRows()
	if firstRun {
		m.flash("hooks installed — status tracking is live", false)
	}
	// Reopen the most recently used session if it survived (e.g. the manager
	// was restarted while claudes kept running).
	if id := m.mostRecentLive(); id != "" {
		m.open(id, false)
	}
	return m
}

func (m *Model) Init() tea.Cmd {
	return tickCmd()
}

func tickCmd() tea.Cmd {
	return tea.Tick(300*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// flash shows a transient footer notice.
func (m *Model) flash(msg string, isErr bool) {
	m.notice = msg
	m.noticeErr = isErr
	m.noticeUntil = time.Now().Add(4 * time.Second)
}

// save persists state immediately; failures surface as a notice.
func (m *Model) save() {
	if err := m.st.Save(m.statePath); err != nil {
		m.flash("failed to save state: "+err.Error(), true)
		return
	}
	m.dirty = false
}

// ---- viewport & layout ---------------------------------------------------

// sidebarWidth is the target width of the sidebar pane: the user's saved
// preference, clamped to the window.
func (m *Model) sidebarWidth(totalW int) int {
	w := m.st.SidebarWidth
	if w == 0 {
		// Wide enough that every column — cost included — is on by default;
		// a desk that hides a feature until a divider is dragged doesn't have
		// that feature.
		w = 46
	}
	if maxW := totalW - 30; w > maxW {
		w = maxW
	}
	if w < 20 {
		w = 20
	}
	return w
}

func (m *Model) placeholderCmd() string {
	return shellQuote(m.selfBin) + " __viewport"
}

func attachCmd(tmuxName string) string {
	return "TMUX= exec tmux attach-session -t " + shellQuote("="+tmuxName)
}

// ensureLayout finds/creates the viewport pane next to this sidebar pane.
func (m *Model) ensureLayout() {
	m.sidebarPane = os.Getenv("TMUX_PANE")
	if m.sidebarPane != "" {
		_ = tmuxctl.SetPaneRole(m.sidebarPane, "sidebar")
	}
	panes, err := tmuxctl.Panes()
	if err != nil {
		return
	}
	for _, p := range panes {
		if p.Role == "viewport" || (p.ID != m.sidebarPane && p.Role == "") {
			m.vpPane, m.vpTTY = p.ID, p.TTY
			_ = tmuxctl.SetPaneRole(p.ID, "viewport")
		}
	}
	if m.vpPane == "" && m.sidebarPane != "" {
		if id, err := tmuxctl.SplitViewport(m.sidebarPane, m.placeholderCmd()); err == nil {
			m.vpPane = id
			_ = tmuxctl.SetPaneRole(id, "viewport")
			_ = tmuxctl.SetPaneOption(id, "remain-on-exit", "on")
			if panes, err := tmuxctl.Panes(); err == nil {
				for _, p := range panes {
					if p.ID == id {
						m.vpTTY = p.TTY
					}
				}
			}
		}
	}
	if m.vpPane != "" {
		_ = tmuxctl.SetPaneOption(m.vpPane, "remain-on-exit", "on")
	}
}

// reconcileViewport keeps pane sizes right, tracks which session the nested
// client actually shows (tab clicks change it behind our back), and recovers
// from the viewport dying (active session killed, client detached).
func (m *Model) reconcileViewport() {
	panes, err := tmuxctl.Panes()
	if err != nil {
		return
	}
	var vp, sb *tmuxctl.PaneInfo
	total := 0
	for i := range panes {
		total += panes[i].W
		switch panes[i].ID {
		case m.vpPane:
			vp = &panes[i]
		case m.sidebarPane:
			sb = &panes[i]
		}
	}
	// Where will keys land? tmux knows exactly; the header dims when the
	// answer is "not here". Polled rather than event-driven because focus
	// reports don't reach a pane whose clients are nested tmux clients.
	if sb != nil {
		m.unfocused = !sb.Active
	}
	total += len(panes) - 1 // pane dividers
	// Sidebar width: on a window resize, restore the preferred width; when
	// the window is unchanged but the pane width differs, the user dragged
	// the divider — adopt that as the new preference.
	if len(panes) > 1 && sb != nil {
		want := m.sidebarWidth(total)
		switch {
		case total != m.lastTotalW:
			if sb.W != want {
				_ = tmuxctl.ResizePane(m.sidebarPane, want)
			}
		case sb.W != want:
			m.st.SidebarWidth = sb.W
			m.dirty = true
		}
		m.lastTotalW = total
	}
	if vp == nil {
		// viewport pane vanished entirely (killed manually): recreate
		m.vpPane, m.vpTTY = "", ""
		m.ensureLayout()
		return
	}
	m.vpTTY = vp.TTY

	derived := ""
	if sess, ok := tmuxctl.ClientSessions()[m.vpTTY]; ok {
		derived = state.SessionIDFromTmux(sess)
	}
	if vp.Dead || (derived == "" && m.activeID != "") {
		// The active session ended or the inner client detached: move to the
		// most recent other live session, else show the placeholder.
		if next := m.mostRecentLive(); next != "" {
			m.open(next, false)
		} else {
			m.activeID = ""
			_ = tmuxctl.RespawnPane(m.vpPane, m.placeholderCmd())
		}
		return
	}
	if derived != "" && derived != m.activeID {
		// Switched via tab click or `agentdeck _tab`: adopt it.
		m.activeID = derived
		m.sawSession(derived)
	}
}

// sawSession records that the user is now looking at a session; seeing it
// dismisses its alert (whatever it was, it's on screen now).
func (m *Model) sawSession(id string) {
	s := m.st.Session(id)
	if s == nil {
		return
	}
	s.LastOpenedAt = time.Now()
	m.dirty = true
	m.clearAlert(id)
}

// clearAlert dismisses a session's attention/needs-you flag.
func (m *Model) clearAlert(id string) {
	if r, ok := m.runtime[id]; ok && (r.Status == status.Attention || r.Status == status.NeedsYou) {
		r.Status = status.Idle
		r.Message = ""
		_ = status.Write(paths.StatusDir(), id, r)
		m.runtime[id] = r
	}
}

// reviveAndOpen takes a session off the old shelf and opens it, after you said
// yes. Split out so open() can ask first without re-entering itself.
func (m *Model) reviveAndOpen(id string, focusViewport bool) {
	s := m.st.Session(id)
	if s == nil {
		return
	}
	s.Archived = false
	m.dirty = true
	m.buildRows()
	m.open(id, focusViewport)
}

// mostRecentLive returns the most recently opened live session, skipping the
// current active one.
func (m *Model) mostRecentLive() string {
	best := ""
	var bestT time.Time
	for i := range m.st.Sessions {
		s := &m.st.Sessions[i]
		if s.ID == m.activeID || !m.isLive(s.ID) {
			continue
		}
		if best == "" || s.LastOpenedAt.After(bestT) {
			best, bestT = s.ID, s.LastOpenedAt
		}
	}
	return best
}

// open makes a session the active one in the viewport, waking it if needed.
// Opening an archived session restores it to its group.
func (m *Model) open(id string, focusViewport bool) {
	s := m.st.Session(id)
	if s == nil {
		return
	}
	// A session on the old shelf was put there deliberately. Reviving one costs
	// real money and a new agent process, and the shelf is exactly where a
	// mis-aimed keypress lands — so ask first, and only when it would actually
	// start something.
	if s.Archived && !m.isLive(id) {
		name := s.Name
		m.confirm("revive "+name+"?", func() tea.Cmd {
			m.reviveAndOpen(id, focusViewport)
			return nil
		})
		return
	}
	if s.Archived {
		s.Archived = false
		m.dirty = true
		m.buildRows()
	}
	if !m.isLive(id) {
		if err := m.wake(s); err != nil {
			m.flash("can't wake "+s.Name+": "+err.Error(), true)
			return
		}
	}
	if m.vpPane == "" {
		m.ensureLayout()
	}
	if m.vpPane == "" {
		m.flash("no viewport pane available", true)
		return
	}
	name := state.TmuxName(id)
	if sess, ok := tmuxctl.ClientSessions()[m.vpTTY]; ok && state.SessionIDFromTmux(sess) != "" {
		if err := tmuxctl.SwitchClientOn(m.vpTTY, name); err != nil {
			m.flash("switch failed: "+err.Error(), true)
			return
		}
	} else {
		if err := tmuxctl.RespawnPane(m.vpPane, attachCmd(name)); err != nil {
			m.flash("open failed: "+err.Error(), true)
			return
		}
	}
	m.activeID = id
	m.sawSession(id)
	m.updateTabs()
	if focusViewport {
		_ = tmuxctl.SelectPane(m.vpPane)
	}
}

// ---- tab bar ---------------------------------------------------------

// tabItem is one element of the tab strip: a live session, or a whole
// collapsed group folded into a single tab.
type tabItem struct {
	kind rowKind
	id   string
}

// tabItems builds the tab strip: live sessions in display order, with
// members of collapsed groups folded into one group tab (Chrome-style).
func (m *Model) tabItems() []tabItem {
	seen := map[string]bool{}
	var out []tabItem
	for _, id := range m.orderedLive() {
		s := m.st.Session(id)
		if g := m.st.Group(s.GroupID); g != nil && g.Collapsed {
			if !seen[g.ID] {
				seen[g.ID] = true
				out = append(out, tabItem{rowGroup, g.ID})
			}
			continue
		}
		out = append(out, tabItem{rowSession, id})
	}
	return out
}

// truncRunes shortens s to at most maxRunes runes, ellipsized. The width the
// result occupies is measured separately (a rune is not a cell), so this only
// caps how much of a long name a tab may spend.
func truncRunes(s string, maxRunes int) string {
	if r := []rune(s); len(r) > maxRunes {
		return string(r[:maxRunes-1]) + "…"
	}
	return s
}

// tabSeg is one rendered tab: its tmux format string, the cells it occupies
// on screen, and what its most urgent member wants — so an off-screen alert
// can still color the overflow chip that hides it.
type tabSeg struct {
	body   string
	width  int
	active bool
	alert  status.Kind // NeedsYou, Attention, or "" for neither
}

// updateTabs renders the tab bar into the outer status line, only touching
// tmux when it changed. Grouped tabs carry their group's colored rail;
// collapsed groups fold into one colored tab; a session that needs you gets
// a BLINKING red tab; unseen output gets a steady yellow label.
//
// The strip is windowed to the tmux window's width: the active tab is always
// kept on screen, and tabs past either edge collapse into clickable ‹N / N›
// chips (they step to the previous/next session, so a hidden tab is always
// two clicks away at most). The chips inherit the most urgent hidden status —
// an alert never scrolls out of sight entirely.
func (m *Model) updateTabs() {
	blinkOn := (m.spin/3)%2 == 0 // ~0.9s phase
	var segs []tabSeg
	for _, it := range m.tabItems() {
		var seg tabSeg
		if it.kind == rowGroup {
			seg = m.renderGroupTab(it.id, blinkOn)
		} else {
			seg = m.renderSessionTab(it.id, blinkOn)
		}
		if seg.body != "" {
			segs = append(segs, seg)
		}
	}

	const managerW = 3 // " ☰ "
	widths := make([]int, len(segs))
	active := -1
	for i, s := range segs {
		widths[i] = s.width
		if s.active {
			active = i
		}
	}
	start, end := tabWindow(widths, active, m.tabScroll, m.lastTotalW-managerW)
	m.tabScroll = start

	var b strings.Builder
	b.WriteString("#[range=user|manager]#[fg=colour37,bold] ☰ #[default]#[norange]")
	if start > 0 {
		b.WriteString(overflowChip("prev", "‹", start, worstAlert(segs[:start]), blinkOn))
	}
	for _, s := range segs[start:end] {
		b.WriteString(s.body)
	}
	if end < len(segs) {
		b.WriteString(overflowChip("next", "›", len(segs)-end, worstAlert(segs[end:]), blinkOn))
	}
	tabs := b.String()
	if tabs == m.tabCache {
		return
	}
	if tmuxctl.SetStatusFormat(tabs) == nil {
		m.tabCache = tabs
	}
}

// chipWidth is the cells an overflow chip occupies: "‹12 " or " 12›".
func chipWidth(hidden int) int {
	return len(strconv.Itoa(hidden)) + 2
}

// overflowChip renders the ‹N / N› marker for tabs hidden past one edge.
// Clicking it steps one session in that direction, which also scrolls the
// strip. It takes on the most urgent hidden status so a needs-you can blink
// from behind the edge.
func overflowChip(dir, arrow string, hidden int, alert status.Kind, blinkOn bool) string {
	fg, bg, bold := "colour244", "colour234", ""
	switch {
	case alert == status.NeedsYou && blinkOn:
		fg, bg, bold = "colour231", "colour160", ",bold"
	case alert == status.NeedsYou:
		fg, bold = "colour203", ",bold"
	case alert == status.Attention:
		fg, bold = "colour221", ",bold"
	}
	label := arrow + strconv.Itoa(hidden) + " "
	if dir == "next" {
		label = " " + strconv.Itoa(hidden) + arrow
	}
	return "#[range=user|tabs:" + dir + "]#[fg=" + fg + ",bg=" + bg + bold + "]" +
		label + "#[default]#[norange]"
}

// worstAlert returns the most urgent status among segments.
func worstAlert(segs []tabSeg) status.Kind {
	worst := status.Kind("")
	for _, s := range segs {
		if s.alert == status.NeedsYou {
			return status.NeedsYou
		}
		if s.alert == status.Attention {
			worst = status.Attention
		}
	}
	return worst
}

// tabWindow picks the visible slice [start, end) of the tab strip. The
// active tab must be inside it; prefStart (last tick's window) keeps the
// strip from jumping around; avail is the cells the tabs may occupy, chips
// included. avail <= 0 means the width is unknown — show everything.
func tabWindow(widths []int, active, prefStart, avail int) (start, end int) {
	n := len(widths)
	if n == 0 {
		return 0, 0
	}
	total := 0
	for _, w := range widths {
		total += w
	}
	if avail <= 0 || total <= avail {
		return 0, n
	}
	start = prefStart
	if start < 0 {
		start = 0
	}
	if start > n-1 {
		start = n - 1
	}
	if active >= 0 && active < start {
		start = active
	}
	fit := func(start int) int {
		budget := avail
		if start > 0 {
			budget -= chipWidth(start)
		}
		end := start
		used := 0
		for end < n && used+widths[end] <= budget {
			used += widths[end]
			end++
		}
		// Tabs remain past the right edge: their chip needs room too.
		for end < n && used+chipWidth(n-end) > budget && end > start {
			end--
			used -= widths[end]
		}
		if end == start {
			end = start + 1 // always show at least one tab, clipped if need be
		}
		return end
	}
	end = fit(start)
	for active >= end && start < n-1 {
		start++
		end = fit(start)
	}
	return start, end
}

// renderSessionTab renders one live session's tab.
func (m *Model) renderSessionTab(id string, blinkOn bool) tabSeg {
	s := m.st.Session(id)
	if s == nil {
		return tabSeg{} // vanished since the row list was built
	}
	k := m.statusOf(s.ID)
	glyph, glyphFg := tabGlyph(k, m.spin)
	name := truncRunes(s.Name, 22)

	bg, fg, bold := "colour236", "colour250", ""
	switch {
	case k == status.NeedsYou && s.ID != m.activeID && blinkOn:
		bg, fg, glyphFg, bold, glyph = "colour160", "colour231", "colour231", ",bold", "◆"
	case k == status.NeedsYou && s.ID != m.activeID:
		fg, glyphFg, bold, glyph = "colour203", "colour203", ",bold", "◆"
	case k == status.NeedsYou && s.ID == m.activeID:
		bg, fg, glyphFg, bold, glyph = "colour88", "colour231", "colour231", ",bold", "◆"
	case s.ID == m.activeID:
		bg, fg, glyphFg, bold = "colour24", "colour254", "colour254", ",bold"
	case k == status.Attention:
		fg, glyphFg, bold, glyph = "colour221", "colour221", ",bold", "●"
	}
	var b strings.Builder
	b.WriteString("#[range=user|" + s.ID + "]")
	if g := m.st.Group(s.GroupID); g != nil {
		b.WriteString("#[fg=" + groupTmux(g.Color) + ",bg=" + bg + "]▎")
	} else {
		b.WriteString("#[bg=" + bg + "] ")
	}
	b.WriteString("#[fg=" + glyphFg + ",bg=" + bg + bold + "]" + glyph +
		" #[fg=" + fg + "]" + strings.ReplaceAll(name, "#", "##") + " #[default]#[norange] ")
	seg := tabSeg{body: b.String(), active: s.ID == m.activeID,
		// rail/space + glyph + space + name + space + separator
		width: 5 + ansi.StringWidth(name)}
	if k == status.NeedsYou || k == status.Attention {
		seg.alert = k
	}
	return seg
}

// renderGroupTab folds a collapsed group into one tab: colored, with its
// live-member count; it blinks when a hidden member needs you.
func (m *Model) renderGroupTab(gid string, blinkOn bool) tabSeg {
	g := m.st.Group(gid)
	if g == nil {
		return tabSeg{}
	}
	liveN, needs, attn, holdsActive := 0, 0, 0, false
	for _, sid := range m.membersOf(gid) {
		if sid == m.activeID {
			holdsActive = true
		}
		switch m.statusOf(sid) {
		case status.NeedsYou:
			needs++
			liveN++
		case status.Attention:
			attn++
			liveN++
		case status.Working, status.Idle:
			liveN++
		}
	}
	name := truncRunes(g.Name, 18)
	bg, fg := "colour236", groupTmux(g.Color)
	if needs > 0 && blinkOn {
		bg, fg = "colour160", "colour231"
	}
	seg := "#[range=user|grp:" + gid + "]" +
		"#[fg=" + fg + ",bg=" + bg + ",bold] ▸ " + strings.ReplaceAll(name, "#", "##") +
		" " + fmt.Sprint(liveN)
	// " ▸ name N" + trailing space + separator
	width := 4 + ansi.StringWidth(name) + len(fmt.Sprint(liveN)) + 2
	if needs > 0 {
		seg += " ◆"
		width += 2
	} else if attn > 0 {
		seg += "#[fg=colour221] ●"
		width += 2
	}
	out := tabSeg{body: seg + " #[default]#[norange] ", width: width, active: holdsActive}
	if needs > 0 {
		out.alert = status.NeedsYou
	} else if attn > 0 {
		out.alert = status.Attention
	}
	return out
}

// tabGlyph returns the status glyph and its tmux color for tab rendering.
func tabGlyph(k status.Kind, spin int) (string, string) {
	switch k {
	case status.Working:
		return spinnerFrames[spin%len(spinnerFrames)], "colour44"
	case status.NeedsYou:
		return "◆", "colour203"
	case status.Attention:
		return "●", "colour221"
	default:
		return "·", "colour244"
	}
}

// drainCmds applies one-shot commands queued by helper subprocesses (tab
// drag drops) — they can't write state.json themselves, the manager is its
// single writer.
func (m *Model) drainCmds() {
	dir := paths.CmdDir()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		os.Remove(p)
		if err != nil {
			continue
		}
		var cmd struct{ Op, From, To string }
		if json.Unmarshal(data, &cmd) != nil {
			continue
		}
		switch cmd.Op {
		case "expand-group":
			if g := m.st.Group(cmd.To); g != nil && g.Collapsed {
				g.Collapsed = false
				m.dirty = true
				m.buildRows()
				m.updateTabs()
			}
		case "tabmenu":
			// A right-click on a tab opens the same menu a right-click on the
			// row does, selecting that row first so the menu names it.
			if m.st.Session(cmd.To) != nil {
				for i, r := range m.rows {
					if r.kind == rowSession && r.id == cmd.To {
						m.sel = i
						m.ensureVisible()
						break
					}
				}
				// The click happened on the tab bar while focus was in the
				// viewport, so keys would go to the agent. Take focus, or the
				// menu is a picture you cannot answer.
				if m.sidebarPane != "" {
					_ = tmuxctl.SelectPane(m.sidebarPane)
				}
				m.openContextMenu()
			}
		case "focus":
			// A notification was clicked: show that session and treat it as
			// seen, exactly like clicking its tab.
			if m.st.Session(cmd.To) != nil {
				m.open(cmd.To, true)
				m.clearAlert(cmd.To)
				for i, r := range m.rows {
					if r.kind == rowSession && r.id == cmd.To {
						m.sel = i
						m.ensureVisible()
						break
					}
				}
			}
		case "archive":
			m.archive(cmd.From)
		case "tabdrop":
			if !m.sortManual() {
				m.flash("sorted by "+m.st.SortMode+" — tab order follows the sort", false)
				continue
			}
			if gid, ok := strings.CutPrefix(cmd.To, "grp:"); ok {
				// dropped onto a collapsed group's tab: join that group
				if m.st.Group(gid) != nil {
					m.st.MoveToGroupFront(cmd.From, gid)
					m.dirty = true
					m.buildRows()
					m.updateTabs()
				}
				continue
			}
			fi, ti := -1, -1
			for i := range m.st.Sessions {
				switch m.st.Sessions[i].ID {
				case cmd.From:
					fi = i
				case cmd.To:
					ti = i
				}
			}
			if fi < 0 || ti < 0 {
				continue
			}
			m.st.MoveSessionNextTo(cmd.From, cmd.To, fi < ti)
			m.dirty = true
			m.buildRows()
			m.updateTabs()
		}
	}
}

// ---- auto-naming -----------------------------------------------------

type nameProbe struct {
	path  string
	mtime time.Time
	title string
	info  agents.Info      // title, model, tokens, window, todos, phase
	cost  agents.CostState // incremental spend accounting
}

// syncNames adopts Claude's own session name for every session the user
// hasn't renamed: the live registry name (exactly what claude's status
// badge shows) when the session is running, else the newest name/summary in
// its transcript. Runs on a slow cadence; transcripts re-read only on mtime
// change.
func (m *Model) syncNames() {
	changed := false
	for i := range m.st.Sessions {
		s := &m.st.Sessions[i]
		prov := agents.Get(s.AgentOf())
		if s.SessionID == "" {
			// Some agents (Codex) only announce their conversation ID when a
			// turn ends. If one is running, find what it started here.
			if !m.isLive(s.ID) {
				continue
			}
			since := s.LastOpenedAt
			if since.IsZero() {
				since = s.CreatedAt
			}
			id := prov.Adopt(s.Dir, since)
			if id == "" {
				continue
			}
			s.SessionID = id
			m.dirty = true
			changed = true
		}
		// Probe the transcript (mtime-gated) for title, tokens, and cost.
		p := m.nameProbes[s.ID]
		if p == nil {
			p = &nameProbe{}
			m.nameProbes[s.ID] = p
		}
		if p.path == "" {
			p.path = prov.TranscriptPath(s.SessionID)
		}
		if p.path != "" {
			if fi, err := os.Stat(p.path); err != nil {
				p.path = "" // transcript moved (e.g. new session id); re-find
			} else if !fi.ModTime().Equal(p.mtime) {
				p.mtime = fi.ModTime()
				p.info = prov.Probe(p.path)
				p.title = p.info.Title
				p.cost = prov.Cost(p.path, p.cost)
			}
		}
		// The agent's live name (Claude's badge registry, Codex's session
		// index) and explicit rename records are authoritative; derived titles
		// (summary / first message) only apply while the session never had an
		// authoritative name — otherwise a closed tab would "revert" when the
		// rename record scrolls out of the probe window.
		name, explicit := prov.LiveName(s.SessionID), true
		if name == "" {
			name = p.title
			explicit = p.info.TitleExplicit
		}
		// A rename inside the agent is the newest thing the user said about
		// this session, so it wins over a name pinned here. Derived titles
		// never override a pin.
		if s.NamedByUser && !explicit {
			continue
		}
		if name == "" || name == s.Name {
			if explicit && name != "" && !s.NameExplicit {
				s.NameExplicit = true
				m.dirty = true
			}
			continue
		}
		if !explicit && s.NameExplicit {
			continue // never downgrade an authoritative name
		}
		s.Name = name
		s.NameExplicit = explicit
		if explicit {
			s.NamedByUser = false // the agent owns the name now
		}
		changed = true
	}
	if changed {
		m.dirty = true
		m.buildRows()
		m.updateTabs()
	}
}

// probeInfo returns everything the transcript probe knows about a session.
func (m *Model) probeInfo(id string) agents.Info {
	if p := m.nameProbes[id]; p != nil {
		return p.info
	}
	return agents.Info{}
}

// tokensOf returns the session's last known context size in tokens (0 =
// unknown).
func (m *Model) tokensOf(id string) int { return m.probeInfo(id).ContextTokens }

// costOf returns the session's estimated total spend in USD (0 = unknown).
func (m *Model) costOf(id string) float64 {
	if p := m.nameProbes[id]; p != nil {
		return p.cost.Total
	}
	return 0
}

// paneModelRe matches a model CHIP in an agent's status line, not the word
// "sonnet" in prose: every chip carries a version ("Opus 5", "Sonnet 4.8",
// "gpt-5.6"). Without the version requirement a conversation that merely
// discusses models renamed the badge, and the pane value outranks the
// transcript — so a session running Opus could show up as sonnet.
var paneModelRe = regexp.MustCompile(`(?i)\b(fable|opus|sonnet|haiku)[ -]?([0-9][0-9.]*)|\b(gpt-[0-9][0-9.]*[a-z-]*|codex[a-z0-9.-]*)\b`)

// syncLiveModels reads each live session's model family straight from
// claude's own status bar in the pane — it reflects /model switches
// instantly, where the transcript only knows after the next turn.
func (m *Model) syncLiveModels() {
	for i := range m.st.Sessions {
		s := &m.st.Sessions[i]
		if !m.isLive(s.ID) {
			delete(m.liveModels, s.ID)
			continue
		}
		text, err := tmuxctl.CapturePanePlain(state.TmuxName(s.ID))
		if err != nil {
			continue
		}
		// The chip lives in the status area at the very bottom; looking further
		// up is how conversation text got mistaken for it.
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		if len(lines) > 3 {
			lines = lines[len(lines)-3:]
		}
		mm := paneModelRe.FindAllStringSubmatch(strings.Join(lines, "\n"), -1)
		if len(mm) == 0 {
			continue
		}
		// Last occurrence: the status bar is the lowest thing on screen. The
		// version is required for the match but is NOT part of the badge —
		// the column shows a family ("opus"), not "opus 4.8".
		last := mm[len(mm)-1]
		fam := last[1] // fable | opus | sonnet | haiku
		if fam == "" {
			fam = last[3] // gpt-… | codex…
		}
		if fam != "" {
			m.liveModels[s.ID] = agents.ShortFamily(strings.ToLower(fam))
		}
	}
}

// familyOf is the model family to display: the live pane wins, then the
// transcript.
func (m *Model) familyOf(id string) string {
	if f, ok := m.liveModels[id]; ok && f != "" {
		return f
	}
	return m.probeInfo(id).Family
}

// contextWindowOf is the model's real context window when the agent reports
// one (Codex does), else a per-family guess.
func (m *Model) contextWindowOf(id string) int {
	if w := m.probeInfo(id).ContextWindow; w > 0 {
		return w
	}
	return modelWindow(m.familyOf(id))
}

// ---- data refresh ----------------------------------------------------

// refresh re-reads live tmux sessions and hook statuses, and syncs any newly
// learned Claude session IDs into persistent state (needed for --resume).
func (m *Model) refresh() {
	m.live = tmuxctl.ListSessions()
	m.runtime = status.ReadAll(paths.StatusDir())
	known := map[string]bool{}
	for i := range m.st.Sessions {
		s := &m.st.Sessions[i]
		known[s.ID] = true
		if r, ok := m.runtime[s.ID]; ok && r.ClaudeSessionID != "" && r.ClaudeSessionID != s.SessionID {
			s.SessionID = r.ClaudeSessionID
			m.dirty = true
		}
	}
	// GC status files with no desk entry: a deleted session's dying agent
	// fires a final event that can resurrect its file. Only files that have
	// gone quiet are removed — a status still being written belongs to
	// something live (another manager, a session mid-migration), and deleting
	// it would destroy state we don't own.
	for id, r := range m.runtime {
		if known[id] {
			continue
		}
		if !r.UpdatedAt.IsZero() && time.Since(r.UpdatedAt) < time.Minute {
			continue // still active: leave it alone
		}
		status.Remove(paths.StatusDir(), id)
		delete(m.runtime, id)
	}

}

// isLive reports whether the session's tmux session exists.
func (m *Model) isLive(id string) bool {
	_, ok := m.live[state.TmuxName(id)]
	return ok
}

// statusOf derives the effective status of a session.
// clearActiveAlert marks the session in the viewport as seen. It runs after
// notifyAlerts, because clearing first would delete the very alert we are
// supposed to announce.
func (m *Model) clearActiveAlert() {
	if m.activeID == "" {
		return
	}
	m.clearAlert(m.activeID)
}

// notifyAlerts posts a desktop notification when a session starts asking for
// you, so you learn about it while looking at another app. Clicking the
// notification runs `agentdeck focus <id>`, which comes back through drainCmds.
//
// Deliberately quiet: nothing fires for the session already on screen (you can
// see it), nothing fires twice for the same alert, and the alerts that exist
// when the manager starts only seed the map — otherwise every restart would
// dump a stack of notifications for sessions you already know about.
func (m *Model) notifyAlerts() {
	if m.notified == nil {
		m.notified = map[string]status.Kind{}
	}
	if m.st.NotifyMuted {
		// Muted: keep the map current so unmuting doesn't fire a backlog.
		for i := range m.st.Sessions {
			id := m.st.Sessions[i].ID
			if k := m.statusOf(id); k == status.NeedsYou || k == status.Attention {
				m.notified[id] = k
			} else {
				delete(m.notified, id)
			}
		}
		m.notifyReady = true
		return
	}
	seeding := !m.notifyReady
	m.notifyReady = true

	for i := range m.st.Sessions {
		s := &m.st.Sessions[i]
		k := m.statusOf(s.ID)
		if k != status.NeedsYou && k != status.Attention {
			delete(m.notified, s.ID) // quiet again: re-arm
			continue
		}
		if s.ID == m.activeID {
			continue // it is on your screen already
		}
		if m.notified[s.ID] == k {
			continue // already announced
		}
		m.notified[s.ID] = k
		if seeding {
			continue
		}
		kind := notify.Finished
		if k == status.NeedsYou {
			kind = notify.NeedsYou
		}
		_ = notify.Send(m.selfBin, notify.Alert{
			SessionID: s.ID,
			Name:      s.Name,
			Folder:    shortDir(s.Dir),
			Agent:     agents.Get(s.AgentOf()).Label(),
			Kind:      kind,
		})
	}
}

func (m *Model) statusOf(id string) status.Kind {
	if !m.isLive(id) {
		return status.Dormant
	}
	r, hasEvent := m.runtime[id]
	hasEvent = hasEvent && r.Status != ""

	// Agents that record their lifecycle in the transcript (Codex) report a
	// status even with no hook/notify event.
	var phase status.Kind
	if s := m.st.Session(id); s != nil {
		phase = agents.Get(s.AgentOf()).StatusFromPhase(m.probeInfo(id).Phase)
	}
	if phase != "" {
		switch {
		case !hasEvent:
			return phase
		case r.Status == status.NeedsYou:
			// Never let a background probe downgrade a blocking request: it
			// clears when you look at it, not because the log moved on.
		case m.probeMTime(id).After(r.UpdatedAt):
			return phase // the transcript is the fresher signal
		}
	}
	if hasEvent {
		return r.Status
	}
	return status.Idle
}

// probeMTime is when the session's transcript last changed (zero if unknown).
func (m *Model) probeMTime(id string) time.Time {
	if p := m.nameProbes[id]; p != nil {
		return p.mtime
	}
	return time.Time{}
}

// counts returns (working, needsYou, attention).
func (m *Model) counts() (working, needsYou, attention int) {
	for i := range m.st.Sessions {
		switch m.statusOf(m.st.Sessions[i].ID) {
		case status.Working:
			working++
		case status.NeedsYou:
			needsYou++
		case status.Attention:
			attention++
		}
	}
	return
}

// ---- session lifecycle -------------------------------------------------

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// wake starts the tmux session for a dormant desk entry, resuming its Claude
// conversation when its ID is known (falling back to a fresh one).
func (m *Model) wake(s *state.Session) error {
	if fi, err := os.Stat(s.Dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("directory no longer exists: %s", s.Dir)
	}
	p := agents.Get(s.AgentOf())
	if !p.Installed() {
		return fmt.Errorf("%s is not installed", p.Label())
	}
	bin := shellQuote(p.Binary())
	cmd := bin
	if s.SessionID != "" {
		// Fall back to a fresh session ONLY if resume fails immediately
		// (bad/expired id). A resumed session that runs fine and crashes an
		// hour later must NOT be silently replaced by a fresh conversation —
		// the pane just closes and the entry goes dormant with its resume id
		// intact.
		resume := bin
		for _, a := range p.ResumeArgs(s.SessionID) {
			resume += " " + shellQuote(a)
		}
		cmd = fmt.Sprintf(
			"_t0=$(date +%%s); %s; _rc=$?; "+
				"if [ $_rc -ne 0 ] && [ $(($(date +%%s)-_t0)) -lt 10 ]; then "+
				"echo; echo 'agentdeck: resume failed, starting a fresh session'; exec %s; fi",
			resume, bin)
	}
	name := state.TmuxName(s.ID)
	env := map[string]string{
		"AGENTDECK_ID": s.ID,
		// Hooks and notify programs inside the session must write where this
		// manager reads, even when AGENTDECK_HOME is customized.
		"AGENTDECK_HOME":  paths.Home(),
		"AGENTDECK_AGENT": p.Kind(),
	}
	if err := tmuxctl.NewSession(name, s.Dir, env, cmd); err != nil {
		return err
	}
	tmuxctl.ConfigureAgentSession(name)
	_ = status.Write(paths.StatusDir(), s.ID, status.Runtime{
		Status:          status.Idle,
		ClaudeSessionID: s.SessionID,
	})
	m.live = tmuxctl.ListSessions()
	return nil
}

// sleep kills the tmux session but keeps the desk entry for later resume.
func (m *Model) sleep(id string) {
	s := m.st.Session(id)
	if s == nil || !m.isLive(id) {
		return
	}
	if err := tmuxctl.KillSession(state.TmuxName(id)); err != nil {
		m.flash("sleep failed: "+err.Error(), true)
		return
	}
	delete(m.live, state.TmuxName(id))
	m.flash(s.Name+" is dormant — enter wakes it with --resume", false)
	m.reconcileViewport()
	m.updateTabs()
}

// archive closes a session Chrome-style: the process is killed but the
// entry drops into the "old" section, resumable any time.
func (m *Model) archive(id string) {
	s := m.st.Session(id)
	if s == nil {
		return
	}
	if m.isLive(id) {
		if err := tmuxctl.KillSession(state.TmuxName(id)); err != nil && tmuxctl.Has(state.TmuxName(id)) {
			m.flash("couldn't kill the session's process — not closing: "+err.Error(), true)
			return
		}
		delete(m.live, state.TmuxName(id))
	}
	s.Archived = true
	m.save()
	m.buildRows()
	m.reconcileViewport()
	m.updateTabs()
	m.flash(s.Name+" moved to old — open it there to bring it back", false)
}

// deleteSession removes a session from the desk entirely. If its live agent
// can't be killed, the entry stays: never hide a still-running process.
func (m *Model) deleteSession(id string) {
	if m.isLive(id) {
		if err := tmuxctl.KillSession(state.TmuxName(id)); err != nil && tmuxctl.Has(state.TmuxName(id)) {
			m.flash("couldn't kill the session's process — not deleting: "+err.Error(), true)
			return
		}
	}
	status.Remove(paths.StatusDir(), id)
	m.st.DeleteSession(id)
	m.save()
	m.buildRows()
	m.reconcileViewport()
	m.updateTabs()
}

// contextGroup is the group new/imported sessions land in: the group of the
// selected row.
func (m *Model) contextGroup() string {
	r := m.selectedRow()
	if r == nil {
		return ""
	}
	if r.kind == rowGroup {
		return r.id
	}
	if s := m.st.Session(r.id); s != nil {
		return s.GroupID
	}
	return ""
}

// createSession adds a desk entry (name defaults to the folder name) and
// opens it.
func (m *Model) createSession(dir, agent string) {
	name := filepath.Base(dir)
	gid := m.contextGroup()
	if g := m.st.Group(gid); g != nil {
		g.Collapsed = false
	}
	id := m.st.AddAgentSession(name, dir, gid, agent)
	m.save()
	m.buildRows()
	m.selectSession(id)
	m.open(id, true)
}

func (m *Model) selectSession(id string) {
	for i, r := range m.rows {
		if r.kind == rowSession && r.id == id {
			m.sel = i
			m.ensureVisible()
			return
		}
	}
}

// ---- update ------------------------------------------------------------

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = max(10, m.width-6)
		m.search.Width = max(10, m.width-12)
		return m, nil

	case tickMsg:
		m.spin++
		m.refresh()
		m.notifyAlerts()
		m.clearActiveAlert()
		m.drainCmds()
		m.reconcileViewport()
		if m.spin%10 == 0 { // every ~3s: cheap (mtime-gated) title sync
			m.syncNames()
			m.syncLiveModels()
		}
		m.updateTabs()
		m.buildRows()
		if m.dirty {
			m.save()
		}
		if time.Now().After(m.noticeUntil) {
			m.notice = ""
		}
		return m, tickCmd()

	case importScanMsg:
		m.scanning = false
		m.importItems = msg.items
		m.importSel, m.importTop = 0, 0
		return m, nil

	case tea.MouseMsg:
		return m.updateMouse(msg)

	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m *Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeNormal, modeSearch:
	case modeGroupPick:
		return m.mousePick(msg)
	case modeImport:
		return m.mouseImport(msg)
	case modeHelp, modeInfo:
		if msg.Action == tea.MouseActionPress && msg.Button != tea.MouseButtonNone {
			m.mode = modeNormal
		}
		return m, nil
	default:
		return m, nil
	}
	rowAt := func(y int) int {
		idx := m.top + y - m.listTopY()
		if idx < 0 || idx >= len(m.rows) {
			return -1
		}
		return idx
	}
	switch {
	case msg.Button == tea.MouseButtonWheelUp && msg.Action == tea.MouseActionPress:
		m.moveSel(-1)
	case msg.Button == tea.MouseButtonWheelDown && msg.Action == tea.MouseActionPress:
		m.moveSel(1)

	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		idx := rowAt(msg.Y)
		if idx < 0 {
			m.pressIdx = -1
			return m, nil
		}
		m.sel = idx
		m.ensureVisible()
		m.pressIdx = idx
		m.dragID, m.dragGroupID, m.dragMoved = "", "", false
		r := m.rows[idx]
		if m.filter != "" {
			return m, nil
		}
		if r.kind == rowSession {
			if s := m.st.Session(r.id); s != nil && !s.Archived && m.sortManual() {
				m.dragID = r.id // candidate for drag & drop
			}
		} else if r.id != oldSection {
			// group order is always manual, whatever the session sort mode
			m.dragGroupID = r.id // whole-group drag
		}

	case msg.Action == tea.MouseActionMotion && (m.dragID != "" || m.dragGroupID != ""):
		idx := rowAt(msg.Y)
		if idx < 0 {
			return m, nil
		}
		if m.dragID != "" && m.rows[idx].id != m.dragID {
			m.dragTo(idx)
		}
		if m.dragGroupID != "" && m.rows[idx].kind == rowGroup &&
			m.rows[idx].id != m.dragGroupID && m.rows[idx].id != oldSection {
			m.dragGroupTo(m.rows[idx].id)
		}

	case msg.Button == tea.MouseButtonRight && msg.Action == tea.MouseActionPress:
		if idx := rowAt(msg.Y); idx >= 0 {
			m.sel = idx
			m.ensureVisible()
			m.openContextMenu()
		}

	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionRelease:
		if (m.dragID != "" || m.dragGroupID != "") && m.dragMoved {
			m.save()
			m.updateTabs()
		} else if idx := rowAt(msg.Y); idx >= 0 && idx == m.pressIdx {
			// plain click: activate
			if m.rows[idx].kind == rowGroup {
				m.toggleGroup(m.rows[idx].id)
			} else {
				m.open(m.rows[idx].id, true)
			}
		}
		m.dragID, m.dragGroupID, m.dragMoved, m.pressIdx = "", "", false, -1
	}
	return m, nil
}

// dragGroupTo places the dragged group at the target group's position.
func (m *Model) dragGroupTo(targetID string) {
	fi, ti := -1, -1
	for i := range m.st.Groups {
		switch m.st.Groups[i].ID {
		case m.dragGroupID:
			fi = i
		case targetID:
			ti = i
		}
	}
	if fi < 0 || ti < 0 || fi == ti {
		return
	}
	g := m.st.Groups[fi]
	m.st.Groups = append(m.st.Groups[:fi], m.st.Groups[fi+1:]...)
	m.st.Groups = append(m.st.Groups, state.Group{})
	copy(m.st.Groups[ti+1:], m.st.Groups[ti:])
	m.st.Groups[ti] = g
	m.dragMoved = true
	m.dirty = true
	m.buildRows()
	for i, r := range m.rows {
		if r.kind == rowGroup && r.id == m.dragGroupID {
			m.sel = i
		}
	}
	m.ensureVisible()
}

// openFolder reveals a session's working folder in the desktop file manager
// (or AGENTDECK_OPEN_CMD). The folder comes from the desk's own record, so it
// works for dormant sessions too — not just the ones with a live process.
func (m *Model) openFolder(id string) {
	s := m.st.Session(id)
	if s == nil {
		return
	}
	if err := reveal.Dir(s.Dir); err != nil {
		m.flash(err.Error(), true)
		return
	}
	m.flash("opened "+shortDir(s.Dir), false)
}

// openScratch reveals where the agent keeps this session's own files — Claude
// Code's per-session scratchpad. Only Claude has one, and only once the session
// has actually written something there.
func (m *Model) openScratch(id string) {
	s := m.st.Session(id)
	if s == nil {
		return
	}
	dir := agents.Get(s.AgentOf()).ScratchDir(s.SessionID)
	if dir == "" {
		m.flash(agents.Get(s.AgentOf()).Label()+": no scratch folder for this session", true)
		return
	}
	if err := reveal.Dir(dir); err != nil {
		m.flash(err.Error(), true)
		return
	}
	m.flash("opened its scratch folder", false)
}

// openContextMenu shows right-click actions for the selected row.
func (m *Model) openContextMenu() {
	r := m.selectedRow()
	if r == nil {
		return
	}
	m.menuRow = *r
	m.pickKind = "menu"
	m.pickItems = m.pickItems[:0]
	if r.kind == rowGroup {
		if r.id == oldSection {
			m.pickItems = append(m.pickItems, pickItem{"g-collapse", "expand / collapse", "space"})
		} else {
			m.pickItems = append(m.pickItems,
				pickItem{"g-rename", "rename", "r"},
				pickItem{"g-color", "change color", "c"},
				pickItem{"g-collapse", "collapse / expand", "space"},
				pickItem{"g-delete", "delete group (sessions stay)", "D"})
		}
	} else if s := m.st.Session(r.id); s != nil {
		// Shortcuts match the keys these actions already have in the sidebar:
		// a menu that renames a key you know is worse than a menu with none.
		m.pickItems = append(m.pickItems,
			pickItem{"open", "open", "↵"},
			pickItem{"peek", "open, keep focus here", "o"},
			pickItem{"info", "info", "v"},
			pickItem{"folder", "open its folder", "f"})
		if agents.Get(s.AgentOf()).ScratchDir(s.SessionID) != "" {
			m.pickItems = append(m.pickItems, pickItem{"scratch", "open its scratch folder", "F"})
		}
		m.pickItems = append(m.pickItems,
			pickItem{"rename", "rename", "r"},
			pickItem{"move", "move to group…", "m"})
		if k := m.statusOf(s.ID); k == status.NeedsYou || k == status.Attention {
			m.pickItems = append(m.pickItems, pickItem{"seen", "clear alert", "c"})
		}
		if s.NamedByUser || s.NameExplicit {
			m.pickItems = append(m.pickItems, pickItem{"unpin", "use the agent's name", "u"})
		}
		if m.isLive(s.ID) {
			m.pickItems = append(m.pickItems, pickItem{"sleep", "close tab (keep on desk)", "z"})
		}
		if s.Archived {
			m.pickItems = append(m.pickItems, pickItem{"restore", "restore from old", "R"})
		} else {
			m.pickItems = append(m.pickItems, pickItem{"archive", "close → old", "x"})
		}
		m.pickItems = append(m.pickItems, pickItem{"delete", "delete forever", "D"})
	}
	m.pickSel = 0
	m.mode = modeGroupPick
}

// execMenu runs a context-menu action.
func (m *Model) execMenu(action string) tea.Cmd {
	m.mode = modeNormal
	id := m.menuRow.id
	switch action {
	case "open":
		m.open(id, true)
	case "peek":
		m.open(id, false)
	case "info":
		m.openInfo(id)
	case "folder":
		m.openFolder(id)
	case "scratch":
		m.openScratch(id)
	case "seen":
		m.clearAlert(id)
	case "unpin":
		// Stop pinning a hand-typed name so the agent's own name takes over
		// again on the next sync.
		if s := m.st.Session(id); s != nil {
			s.NamedByUser, s.NameExplicit = false, false
			m.dirty = true
			m.flash("following the agent's name again", false)
		}
	case "rename", "g-rename":
		m.renameTarget = m.menuRow
		if m.menuRow.kind == rowGroup {
			g := m.st.Group(id)
			if g == nil {
				return nil
			}
			m.startInput(modeRename, "rename group", g.Name)
		} else {
			s := m.st.Session(id)
			if s == nil {
				return nil
			}
			m.startInput(modeRename, "rename session", s.Name)
		}
	case "move":
		m.openGroupPick(id)
	case "sleep":
		m.sleep(id)
	case "archive":
		m.archive(id)
	case "restore":
		if s := m.st.Session(id); s != nil {
			s.Archived = false
			m.save()
			m.buildRows()
		}
	case "delete":
		s := m.st.Session(id)
		if s == nil {
			return nil
		}
		m.confirm(fmt.Sprintf("delete %q forever?", s.Name), func() tea.Cmd {
			m.deleteSession(id)
			return nil
		})
	case "g-color":
		m.openColorPick(id)
	case "g-collapse":
		m.toggleGroup(id)
	case "g-delete":
		g := m.st.Group(id)
		m.confirm(fmt.Sprintf("delete group %q? its sessions stay", g.Name), func() tea.Cmd {
			m.st.DeleteGroup(id)
			m.save()
			m.buildRows()
			return nil
		})
	}
	return nil
}

// openColorPick shows the palette for a group.
func (m *Model) openColorPick(gid string) {
	g := m.st.Group(gid)
	if g == nil {
		return
	}
	m.pickKind = "color"
	m.pickFor = gid
	m.pickItems = m.pickItems[:0]
	for i, name := range groupColorNames {
		m.pickItems = append(m.pickItems, pickItem{id: fmt.Sprint(i), label: name})
	}
	m.pickSel = groupSlot(g.Color)
	m.mode = modeGroupPick
}

func (m *Model) sortManual() bool {
	return m.st.SortMode == "" || m.st.SortMode == "manual"
}

// dragTo live-moves the dragged session while the mouse is held: onto a
// session row it takes that row's place (adopting its group); onto a group
// header it becomes that group's first member.
func (m *Model) dragTo(idx int) {
	target := m.rows[idx]
	cur := -1
	for i, r := range m.rows {
		if r.kind == rowSession && r.id == m.dragID {
			cur = i
		}
	}
	switch {
	case target.kind == rowSession:
		if ts := m.st.Session(target.id); ts == nil || ts.Archived {
			return
		}
		m.st.MoveSessionNextTo(m.dragID, target.id, idx > cur)
	case target.id != oldSection:
		if g := m.st.Group(target.id); g != nil {
			g.Collapsed = false
			m.st.MoveToGroupFront(m.dragID, target.id)
		}
	default:
		return
	}
	m.dragMoved = true
	m.dirty = true
	m.buildRows()
	for i, r := range m.rows {
		if r.kind == rowSession && r.id == m.dragID {
			m.sel = i
		}
	}
	m.ensureVisible()
}

func (m *Model) moveSel(d int) {
	m.sel += d
	m.clampSel()
	m.ensureVisible()
}

func (m *Model) ensureVisible() {
	ih := m.listInnerHeight()
	if ih <= 0 {
		return
	}
	if m.sel < m.top {
		m.top = m.sel
	}
	if m.sel >= m.top+ih {
		m.top = m.sel - ih + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

func (m *Model) toggleGroup(gid string) {
	if gid == oldSection {
		m.st.OldExpanded = !m.st.OldExpanded
		m.dirty = true
		m.buildRows()
		return
	}
	if g := m.st.Group(gid); g != nil {
		g.Collapsed = !g.Collapsed
		m.dirty = true
		m.buildRows()
	}
}

func (m *Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeSearch:
		return m.keySearch(msg)
	case modeInputDir, modeInputGroup, modeRename:
		return m.keyTextInput(msg)
	case modeGroupPick:
		return m.keyGroupPick(msg)
	case modeConfirm:
		return m.keyConfirm(msg)
	case modeImport:
		return m.keyImport(msg)
	case modeHelp, modeInfo:
		m.mode = modeNormal // any key closes
		return m, nil
	}
	return m.keyNormal(msg)
}

// openInfo shows the details popup for a session.
func (m *Model) openInfo(id string) {
	s := m.st.Session(id)
	if s == nil {
		return
	}
	m.infoTarget = id
	m.infoGit = gitInfo(s.Dir)
	m.mode = modeInfo
}

// gitInfo returns "branch" or "branch*" (dirty) for a directory, "" if it
// isn't a git repo.
func gitInfo(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	// A branch name comes from the repository, so it is no more trusted than
	// anything else agentdeck displays.
	branch := sanitize.Line(string(out))
	if st, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); err == nil &&
		len(strings.TrimSpace(string(st))) > 0 {
		branch += "*"
	}
	return branch
}

func (m *Model) keyNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "ctrl+c": // the escape hatch stays instant
		if m.dirty {
			m.save()
		}
		return m, tea.Quit

	case "q":
		// One stray letter must not take the whole desk down — the classic
		// accident is typing at the sidebar believing an agent has focus.
		m.confirm("quit agentdeck? sessions keep running", func() tea.Cmd {
			if m.dirty {
				m.save()
			}
			return tea.Quit
		})

	case "up", "k":
		m.moveSel(-1)
	case "down", "j":
		m.moveSel(1)
	case "g", "home":
		m.sel = 0
		m.ensureVisible()
	case "G", "end":
		m.sel = len(m.rows) - 1
		m.clampSel()
		m.ensureVisible()
	case "pgup":
		m.moveSel(-m.listInnerHeight())
	case "pgdown":
		m.moveSel(m.listInnerHeight())

	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if idx := m.nthSession(int(key[0] - '0')); idx >= 0 {
			m.sel = idx
			m.ensureVisible()
			m.open(m.rows[idx].id, true)
		}

	case "enter", "l":
		r := m.selectedRow()
		if r == nil {
			return m, nil
		}
		if r.kind == rowGroup {
			m.toggleGroup(r.id)
		} else {
			m.open(r.id, true)
		}

	case "o":
		// peek: open in viewport but keep focus in the sidebar
		if id := m.selectedSessionID(); id != "" {
			m.open(id, false)
		}

	case "tab":
		if m.vpPane != "" && m.activeID != "" {
			_ = tmuxctl.SelectPane(m.vpPane)
		}

	case "[", "]":
		d := 1
		if key == "[" {
			d = -1
		}
		m.cycleTab(d)

	case "<", ">":
		w := m.sidebarWidth(m.lastTotalW)
		if key == "<" {
			w -= 2
		} else {
			w += 2
		}
		if w < 20 {
			w = 20
		}
		m.st.SidebarWidth = w
		m.dirty = true
		_ = tmuxctl.ResizePane(m.sidebarPane, w)

	case "a":
		idx := m.nextAlert()
		if idx < 0 && m.filter != "" {
			// The alerting session may be hidden by the filter: clear it.
			m.filter = ""
			m.search.SetValue("")
			m.buildRows()
			idx = m.nextAlert()
		}
		if idx >= 0 {
			m.sel = idx
			m.ensureVisible()
			m.open(m.rows[idx].id, false)
		} else {
			m.flash("nothing needs attention", false)
		}

	case "h":
		r := m.selectedRow()
		if r == nil {
			return m, nil
		}
		if r.kind == rowGroup {
			m.toggleGroup(r.id)
			return m, nil
		}
		if s := m.st.Session(r.id); s != nil {
			header := s.GroupID
			if s.Archived {
				header = oldSection
			}
			for i, rr := range m.rows {
				if rr.kind == rowGroup && rr.id == header {
					m.sel = i
					m.ensureVisible()
					break
				}
			}
		}

	case "s", "S":
		m.openSortPick()

	case "v":
		if id := m.selectedSessionID(); id != "" {
			m.openInfo(id)
		}

	case "M":
		m.st.NotifyMuted = !m.st.NotifyMuted
		m.save()
		if m.st.NotifyMuted {
			m.flash("notifications muted", false)
		} else {
			m.flash("notifications on", false)
		}

	case "f":
		if id := m.selectedSessionID(); id != "" {
			m.openFolder(id)
		}

	case "F":
		if id := m.selectedSessionID(); id != "" {
			m.openScratch(id)
		}

	case "c":
		if r := m.selectedRow(); r != nil && r.kind == rowGroup && r.id != oldSection {
			m.openColorPick(r.id)
		}

	case " ":
		if r := m.selectedRow(); r != nil && r.kind == rowGroup {
			m.toggleGroup(r.id)
		}

	case "/":
		m.mode = modeSearch
		m.search.SetValue("")
		m.filter = ""
		m.search.Focus()
		m.buildRows()

	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.buildRows()
		}

	case "n":
		m.pickFor = ""
		m.startInput(modeInputDir, "directory (tab completes)", m.defaultDir())

	case "N":
		m.pickFor = ""
		m.startInput(modeInputGroup, "new group name", "")

	case "i":
		m.mode = modeImport
		m.scanning = true
		m.importItems = nil
		m.search.SetValue("")
		m.search.Focus()
		exclude := map[string]bool{}
		for i := range m.st.Sessions {
			if sid := m.st.Sessions[i].SessionID; sid != "" {
				exclude[sid] = true
			}
		}
		return m, func() tea.Msg {
			// Every agent's past conversations, newest first across both.
			var all []agents.Conversation
			for _, p := range agents.All() {
				all = append(all, p.Scan(exclude, 400)...)
			}
			sort.SliceStable(all, func(a, b int) bool { return all[a].MTime.After(all[b].MTime) })
			if len(all) > 400 {
				all = all[:400]
			}
			return importScanMsg{items: all}
		}

	case "r":
		r := m.selectedRow()
		if r == nil {
			return m, nil
		}
		m.renameTarget = *r
		if r.kind == rowGroup {
			if g := m.st.Group(r.id); g != nil {
				m.startInput(modeRename, "rename group", g.Name)
			}
		} else if sess := m.st.Session(r.id); sess != nil {
			m.startInput(modeRename, "rename session", sess.Name)
		}

	case "m":
		if id := m.selectedSessionID(); id != "" {
			m.openGroupPick(id)
		}

	case "J", "shift+down":
		m.reorder(1)
	case "K", "shift+up":
		m.reorder(-1)

	case "z": // z as in the zz badge dormant sessions wear
		id := m.selectedSessionID()
		if id == "" || !m.isLive(id) {
			return m, nil
		}
		s := m.st.Session(id)
		// Always confirmed: closing a tab ends the agent's process, and this
		// key gets hit by people who believe they are typing into an agent.
		q := fmt.Sprintf("close %q's tab? it stays on the desk", s.Name)
		if m.statusOf(id) == status.Working {
			q = fmt.Sprintf("%q is still working — close its tab anyway?", s.Name)
		}
		m.confirm(q, func() tea.Cmd {
			m.sleep(id)
			return nil
		})

	case "x", "d", "backspace", "delete":
		r := m.selectedRow()
		if r == nil {
			return m, nil
		}
		if r.kind == rowGroup {
			if r.id == oldSection {
				m.flash("open the section and delete old sessions one by one", false)
				return m, nil
			}
			g := m.st.Group(r.id)
			gid := r.id // capture by value: r aliases the reused rows slice
			m.confirm(fmt.Sprintf("delete group %q? its sessions stay", g.Name), func() tea.Cmd {
				m.st.DeleteGroup(gid)
				m.save()
				m.buildRows()
				return nil
			})
			return m, nil
		}
		s := m.st.Session(r.id)
		id := r.id
		if s.Archived {
			m.confirm(fmt.Sprintf("delete %q forever?", s.Name), func() tea.Cmd {
				m.deleteSession(id)
				return nil
			})
			return m, nil
		}
		m.confirm(fmt.Sprintf("close %q → old?", s.Name), func() tea.Cmd {
			m.archive(id)
			return nil
		})

	case "?":
		m.mode = modeHelp
	}
	return m, nil
}

// cycleTab moves the viewport to the next/previous visible tab (members of
// collapsed groups are skipped, matching the strip).
func (m *Model) cycleTab(d int) {
	var liveIDs []string
	for _, it := range m.tabItems() {
		if it.kind == rowSession {
			liveIDs = append(liveIDs, it.id)
		}
	}
	if len(liveIDs) == 0 {
		return
	}
	cur := 0
	for i, id := range liveIDs {
		if id == m.activeID {
			cur = i
		}
	}
	next := (cur + d + len(liveIDs)) % len(liveIDs)
	m.open(liveIDs[next], false)
	m.selectSession(liveIDs[next])
}

// reorder moves the selected session or group up/down.
func (m *Model) reorder(delta int) {
	r := m.selectedRow()
	if r == nil {
		return
	}
	if m.filter != "" {
		m.flash("clear the search filter before reordering", false)
		return
	}
	if r.kind == rowGroup {
		// group order is always manual, whatever the session sort mode
		m.st.MoveGroup(r.id, delta) // no-op for the old section
	} else {
		if !m.sortManual() {
			m.flash("sorted by "+m.st.SortMode+" — press S and pick manual to reorder", false)
			return
		}
		if s := m.st.Session(r.id); s != nil && s.Archived {
			return
		}
		m.st.MoveSession(r.id, delta)
	}
	m.dirty = true
	m.buildRows() // selection follows the item via buildRows ID tracking
	m.ensureVisible()
	m.updateTabs()
}

func (m *Model) startInput(mo mode, prompt, initial string) {
	m.mode = mo
	m.input.Prompt = ""
	m.input.Placeholder = prompt
	m.input.SetValue(initial)
	m.input.CursorEnd()
	m.input.Focus()
}

func (m *Model) confirm(msgText string, fn func() tea.Cmd) {
	m.mode = modeConfirm
	m.confirmMsg = msgText
	m.confirmFn = fn
}

func (m *Model) keyConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		fn := m.confirmFn
		m.mode = modeNormal
		m.confirmFn = nil
		if fn != nil {
			return m, fn()
		}
	case "n", "N", "esc", "q":
		m.mode = modeNormal
		m.confirmFn = nil
	}
	return m, nil
}

func (m *Model) keySearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeNormal
		m.filter = ""
		m.search.Blur()
		m.buildRows()
		return m, nil
	case "enter":
		id := m.selectedSessionID()
		m.mode = modeNormal
		m.search.Blur()
		if id != "" {
			m.open(id, true)
		}
		return m, nil
	case "up", "ctrl+p", "ctrl+k":
		m.moveSel(-1)
		return m, nil
	case "down", "ctrl+n", "ctrl+j":
		m.moveSel(1)
		return m, nil
	}
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	if m.search.Value() != m.filter {
		m.filter = m.search.Value()
		m.buildRows()
		m.sel = m.firstSessionRow()
		m.top = 0
		m.ensureVisible()
	}
	return m, cmd
}

func (m *Model) keyTextInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeNormal
		m.input.Blur()
		m.pickFor = "" // abandon a half-finished move
		return m, nil
	case "tab":
		if m.mode == modeInputDir {
			m.completePath()
		}
		return m, nil
	case "enter":
		val := strings.TrimSpace(m.input.Value())
		return m.submitInput(val)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// completePath does shell-style tab completion (directories only) inside the
// new-session directory prompt.
func (m *Model) completePath() {
	val := m.input.Value()
	expanded := expandHome(val)
	dir, prefix := filepath.Split(expanded)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var cands []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if strings.HasPrefix(e.Name(), prefix) {
			cands = append(cands, e.Name())
		}
	}
	if len(cands) == 0 {
		return
	}
	lcp := cands[0]
	for _, c := range cands[1:] {
		for !strings.HasPrefix(c, lcp) {
			lcp = lcp[:len(lcp)-1]
		}
	}
	completed := filepath.Join(dir, lcp)
	if len(cands) == 1 {
		completed += "/"
	} else {
		m.flash(fmt.Sprintf("%d matches", len(cands)), false)
	}
	m.input.SetValue(completed)
	m.input.CursorEnd()
}

func expandHome(v string) string {
	if v == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(v, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, v[2:])
	}
	return v
}

func (m *Model) submitInput(val string) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeInputDir:
		dir, err := normalizeDir(val)
		if err != nil {
			m.flash(err.Error(), true)
			return m, nil
		}
		m.mode = modeNormal
		m.input.Blur()
		m.pendingDir = dir
		m.openAgentPick()
		return m, nil

	case modeInputGroup:
		if val == "" {
			m.flash("group name can't be empty", true)
			return m, nil
		}
		gid := m.st.AddGroup(val)
		if m.pickFor != "" {
			// "+ new group…" chosen while moving a session: finish the move.
			m.st.MoveToGroup(m.pickFor, gid)
			if s := m.st.Session(m.pickFor); s != nil {
				s.Archived = false
			}
			m.pickFor = ""
		}
		m.save()
		m.mode = modeNormal
		m.input.Blur()
		m.buildRows()
		return m, nil

	case modeRename:
		if val == "" {
			m.flash("name can't be empty", true)
			return m, nil
		}
		if m.renameTarget.kind == rowGroup {
			if g := m.st.Group(m.renameTarget.id); g != nil {
				g.Name = val
			}
		} else if s := m.st.Session(m.renameTarget.id); s != nil {
			s.Name = val
			m.pushRename(s, val)
		}
		m.save()
		m.mode = modeNormal
		m.input.Blur()
		m.buildRows()
		m.updateTabs()
		return m, nil
	}
	m.mode = modeNormal
	return m, nil
}

// pushRename propagates a rename into the Claude session itself so both
// sides show the same name. At a prompt we drive claude's own /rename; when
// the session is busy or dormant we append the same custom-title record
// the agent writes. If the name reached the agent, auto-sync stays ON (the agent
// stays the source of truth); only a purely local rename pins the name.
func (m *Model) pushRename(s *state.Session, name string) {
	// The name is typed into the agent's own prompt, so a stray newline would
	// submit whatever followed it as a separate message.
	name = sanitize.Line(name)
	s.NameExplicit = true // a rename is authoritative wherever it lands
	// Claude accepts /rename at a prompt; Codex has no such command, so its
	// rename stays on the desk.
	if s.AgentOf() == state.AgentClaude {
		if k := m.statusOf(s.ID); k == status.Idle || k == status.Attention {
			if tmuxctl.SendLine(state.TmuxName(s.ID), "/rename "+name) == nil {
				s.NamedByUser = false
				return
			}
		}
	}
	if s.SessionID != "" && agents.Get(s.AgentOf()).Rename(s.SessionID, name) {
		s.NamedByUser = false
		return
	}
	s.NamedByUser = true // nowhere to push: keep the name locally
}

// defaultDir picks a sensible starting directory for a new session.
func (m *Model) defaultDir() string {
	if id := m.selectedSessionID(); id != "" {
		if s := m.st.Session(id); s != nil {
			return s.Dir + "/"
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home + "/"
	}
	return "/"
}

func normalizeDir(v string) (string, error) {
	if v == "" {
		return "", fmt.Errorf("directory can't be empty")
	}
	abs, err := filepath.Abs(expandHome(v))
	if err != nil {
		return "", err
	}
	abs = strings.TrimRight(abs, "/")
	if abs == "" {
		abs = "/"
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("no such directory: %s", abs)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("not a directory: %s", abs)
	}
	return abs, nil
}

// openAgentPick asks which agent a new session should run. Agents that
// aren't installed are listed but marked, so the desk explains itself
// instead of failing at launch.
func (m *Model) openAgentPick() {
	m.pickKind = "agent"
	m.pickItems = m.pickItems[:0]
	for _, p := range agents.All() {
		label := p.Label()
		if !p.Installed() {
			label += " (not installed)"
		}
		m.pickItems = append(m.pickItems, pickItem{id: p.Kind(), label: label})
	}
	// The highlight always starts on the same agent: an explicit picker whose
	// default moved with the cursor would make `n <dir> enter enter` create
	// whichever agent the selected row happened to use.
	m.pickSel = 0
	m.mode = modeGroupPick
}

// openSortPick opens the sort-mode chooser.
func (m *Model) openSortPick() {
	m.pickKind = "sort"
	m.pickItems = []pickItem{
		{id: "manual", label: "manual (J/K, drag)"},
		{id: "status", label: "status — alerts first"},
		{id: "recent", label: "recent activity"},
		{id: "name", label: "name"},
		{id: "folder", label: "folder"},
	}
	m.pickSel = 0
	cur := m.st.SortMode
	if cur == "" {
		cur = "manual"
	}
	for i, it := range m.pickItems {
		if it.id == cur {
			m.pickSel = i
		}
	}
	m.mode = modeGroupPick
}

// openGroupPick opens the group chooser for moving a session.
func (m *Model) openGroupPick(forSession string) {
	m.pickKind = "group"
	m.pickFor = forSession
	m.pickItems = m.pickItems[:0]
	m.pickItems = append(m.pickItems, pickItem{id: "", label: "(no group)"})
	for _, g := range m.st.Groups {
		m.pickItems = append(m.pickItems, pickItem{id: g.ID, label: g.Name})
	}
	m.pickItems = append(m.pickItems, pickItem{id: "+new", label: "+ new group…"})
	m.pickSel = 0
	sess := m.st.Session(forSession)
	if sess == nil {
		return
	}
	cur := sess.GroupID
	for i, it := range m.pickItems {
		if it.id == cur {
			m.pickSel = i
		}
	}
	m.mode = modeGroupPick
}

func (m *Model) keyGroupPick(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.mode = modeNormal
		m.pickFor = ""    // abandon a half-finished move
		m.pendingDir = "" // abandon a half-finished new-session wizard
		return m, nil
	case "up", "k", "ctrl+p":
		if m.pickSel > 0 {
			m.pickSel--
		}
		return m, nil
	case "down", "j", "ctrl+n":
		if m.pickSel < len(m.pickItems)-1 {
			m.pickSel++
		}
		return m, nil
	case "enter":
		return m.pickEnter()
	}
	// Context menus accept the shortcut shown beside each item, so the menu can
	// be used the way the sidebar is: look, press the key you already know.
	if m.pickKind == "menu" {
		if k := msg.String(); k != "" {
			if k == " " {
				k = "space" // menu items label it the way it is spoken
			}
			for i, it := range m.pickItems {
				if it.key != "" && it.key == k {
					m.pickSel = i
					return m.pickEnter()
				}
			}
		}
	}
	return m, nil
}

// pickEnter confirms the highlighted picker item (keyboard enter or mouse
// click).
func (m *Model) pickEnter() (tea.Model, tea.Cmd) {
	if m.pickSel < 0 || m.pickSel >= len(m.pickItems) {
		return m, nil
	}
	it := m.pickItems[m.pickSel]
	if m.pickKind == "menu" {
		return m, m.execMenu(it.id)
	}
	if m.pickKind == "agent" {
		dir := m.pendingDir
		m.pendingDir = ""
		m.mode = modeNormal
		if dir != "" {
			m.createSession(dir, it.id)
		}
		return m, nil
	}
	if m.pickKind == "color" {
		if g := m.st.Group(m.pickFor); g != nil {
			fmt.Sscanf(it.id, "%d", &g.Color)
			m.save()
			m.updateTabs()
		}
		m.pickFor = ""
		m.mode = modeNormal
		m.buildRows()
		return m, nil
	}
	if m.pickKind == "sort" {
		if it.id == "manual" {
			m.st.SortMode = ""
		} else {
			m.st.SortMode = it.id
		}
		m.save()
		m.mode = modeNormal
		m.buildRows()
		m.updateTabs()
		return m, nil
	}
	if it.id == "+new" {
		m.startInput(modeInputGroup, "new group name", "")
		return m, nil
	}
	m.st.MoveToGroup(m.pickFor, it.id)
	if s := m.st.Session(m.pickFor); s != nil {
		s.Archived = false // moving an old session revives its entry
	}
	if g := m.st.Group(it.id); g != nil {
		g.Collapsed = false
	}
	m.pickFor = ""
	m.save()
	m.mode = modeNormal
	m.buildRows()
	return m, nil
}

// mousePick lets the mouse drive the picker overlay: wheel moves, clicking
// an item chooses it, clicking outside cancels.
func (m *Model) mousePick(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Button == tea.MouseButtonWheelUp && msg.Action == tea.MouseActionPress:
		if m.pickSel > 0 {
			m.pickSel--
		}
	case msg.Button == tea.MouseButtonWheelDown && msg.Action == tea.MouseActionPress:
		if m.pickSel < len(m.pickItems)-1 {
			m.pickSel++
		}
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		box := m.viewGroupPick()
		lines := strings.Split(box, "\n")
		boxH, boxW := len(lines), lipgloss.Width(box)
		ih := m.listInnerHeight()
		top := m.listTopY() + max(0, (ih-boxH)/2)
		left := max(0, (m.width-boxW)/2)
		if msg.Y < top || msg.Y >= top+boxH || msg.X < left || msg.X >= left+boxW {
			m.mode = modeNormal // click outside cancels
			m.pickFor = ""
			return m, nil
		}
		// box: border, title, blank, items…, blank, hint, border
		if idx := msg.Y - top - 3; idx >= 0 && idx < len(m.pickItems) {
			m.pickSel = idx
			return m.pickEnter()
		}
	}
	return m, nil
}

// mouseImport lets the mouse drive the import picker: wheel moves, clicking
// an entry imports it.
func (m *Model) mouseImport(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	filtered := m.importFiltered()
	switch {
	case msg.Button == tea.MouseButtonWheelUp && msg.Action == tea.MouseActionPress:
		if m.importSel > 0 {
			m.importSel--
		}
	case msg.Button == tea.MouseButtonWheelDown && msg.Action == tea.MouseActionPress:
		if m.importSel < len(filtered)-1 {
			m.importSel++
		}
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		// items start after 3 header lines in the body, two lines each
		idx := m.importTop + (msg.Y-m.listTopY()-3)/2
		if msg.Y-m.listTopY() >= 3 && idx >= 0 && idx < len(filtered) {
			it := m.importItems[filtered[idx]]
			m.mode = modeNormal
			m.search.SetValue("")
			m.search.Blur()
			m.importConversation(it)
		}
	}
	return m, nil
}

// ---- import picker -------------------------------------------------------

// importFiltered returns indices of importItems matching the search box.
func (m *Model) importFiltered() []int {
	q := strings.TrimSpace(m.search.Value())
	var out []int
	for i := range m.importItems {
		it := &m.importItems[i]
		if q == "" || fuzzyMatch(q, it.Title, it.Dir) {
			out = append(out, i)
		}
	}
	return out
}

func (m *Model) keyImport(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	filtered := m.importFiltered()
	switch msg.String() {
	case "esc":
		m.mode = modeNormal
		m.search.SetValue("")
		m.search.Blur()
		return m, nil
	case "up", "ctrl+p":
		if m.importSel > 0 {
			m.importSel--
		}
		return m, nil
	case "down", "ctrl+n":
		if m.importSel < len(filtered)-1 {
			m.importSel++
		}
		return m, nil
	case "enter":
		if m.importSel >= 0 && m.importSel < len(filtered) {
			it := m.importItems[filtered[m.importSel]]
			m.mode = modeNormal
			m.search.SetValue("")
			m.search.Blur()
			m.importConversation(it)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	m.importSel, m.importTop = 0, 0
	return m, cmd
}

// importConversation adds a past conversation to the desk and opens
// it (which resumes it).
func (m *Model) importConversation(c agents.Conversation) {
	if fi, err := os.Stat(c.Dir); err != nil || !fi.IsDir() {
		m.flash("its directory no longer exists: "+c.Dir, true)
		return
	}
	gid := m.contextGroup()
	id := m.st.AddAgentSession(c.Title, c.Dir, gid, c.Agent)
	s := m.st.Session(id)
	if s == nil { // AddAgentSession just created it; belt and braces
		return
	}
	s.SessionID = c.SessionID
	m.save()
	m.buildRows()
	m.selectSession(id)
	m.open(id, true)
}

// Notef shows a startup notice in the footer (used for migration messages).
func (m *Model) Notef(format string, args ...any) {
	m.flash(fmt.Sprintf(format, args...), false)
}
