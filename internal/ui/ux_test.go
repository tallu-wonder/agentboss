package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/tallu-wonder/agentboss/internal/agents"
	"github.com/tallu-wonder/agentboss/internal/state"
	"github.com/tallu-wonder/agentboss/internal/status"
	"github.com/tallu-wonder/agentboss/internal/tmuxctl"
)

func uxModel(t *testing.T) *Model {
	t.Helper()
	m := testModel()
	m.statePath = filepath.Join(t.TempDir(), "state.json")
	m.input = textinput.New()
	m.search = textinput.New()
	m.live = map[string]tmuxctl.Info{}
	m.runtime = map[string]status.Runtime{}
	m.nameProbes = map[string]*nameProbe{}
	m.Update(tea.WindowSizeMsg{Width: 46, Height: 24})
	return m
}

func TestViewingAcknowledgesWithoutChangingAgentState(t *testing.T) {
	m := uxModel(t)
	id := m.st.Sessions[0].ID
	m.live[state.TmuxName(id)] = tmuxctl.Info{}
	at := time.Now().Add(-time.Minute)
	r := status.Runtime{Status: status.NeedsYou, Message: "Permission required", UpdatedAt: at}
	m.runtime[id] = r
	m.clearAlert(id)
	if m.statusOf(id) != status.NeedsYou || m.runtime[id] != r {
		t.Fatal("viewing changed the blocking request")
	}
	if !m.st.Session(id).SeenAt.After(at) {
		t.Fatal("notification was not marked seen")
	}
	r.Status = status.Working
	r.UpdatedAt = time.Now()
	m.runtime[id] = r
	if m.statusOf(id) != status.Working {
		t.Fatal("a resumed agent remained blocked")
	}
	r.Status = status.Attention
	r.UpdatedAt = time.Now()
	m.runtime[id] = r
	m.clearAlert(id)
	if m.statusOf(id) != status.Idle || m.runtime[id] != r {
		t.Fatal("completion acknowledgement overwrote an agent event")
	}
	r.UpdatedAt = time.Now().Add(time.Second)
	m.runtime[id] = r
	if m.statusOf(id) != status.Attention {
		t.Fatal("a later completion was hidden")
	}
}

func TestFormsShowErrorsAndWorktreeInput(t *testing.T) {
	m := uxModel(t)
	m.startInput(modeInputDir, "directory", "")
	m.submitInput(filepath.Join(t.TempDir(), "missing"))
	if m.mode != modeInputDir || !strings.Contains(ansi.Strip(m.View()), "no such directory") {
		t.Fatal("folder validation is invisible")
	}
	m.wtFlow = true
	m.wtRepo = t.TempDir()
	m.startInput(modeInputWtName, "worktree name", "")
	m.handleInput(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("feature-branch")})
	if !strings.Contains(ansi.Strip(m.View()), "feature-branch") {
		t.Fatal("worktree name is invisible")
	}
	m.handleInput(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.mode != modeInputDir || m.input.Value() != m.wtRepo {
		t.Fatal("Back did not restore the repository")
	}
}

func TestRecentFoldersAndCompletion(t *testing.T) {
	m := uxModel(t)
	dir := t.TempDir()
	for _, name := range []string{"α-one", "α-two"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	m.st.Sessions[0].Dir = dir
	m.startInput(modeInputDir, "directory", dir+"/")
	if len(m.suggestions) == 0 || m.suggestions[0] != dir {
		t.Fatalf("missing recent folder: %v", m.suggestions)
	}
	m.input.SetValue(dir + "/α-")
	m.completeDirectory()
	if len(m.suggestions) != 2 || m.input.Value() != dir+"/α-" {
		t.Fatalf("ambiguous completion lost candidates: %v", m.suggestions)
	}
	m.suggestionSel = 1
	m.completeDirectory()
	if !strings.HasSuffix(m.input.Value(), "α-two/") {
		t.Fatal("selected completion was not used")
	}
}

func TestPanelScrollingAndGeometry(t *testing.T) {
	for _, size := range [][2]int{{46, 24}, {26, 16}, {20, 12}} {
		m := uxModel(t)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.mode = modeHelp
		first := m.View()
		m.keyScroll(tea.KeyMsg{Type: tea.KeyDown})
		if m.mode != modeHelp {
			t.Fatal("Down dismissed help")
		}
		m.keyScroll(tea.KeyMsg{Type: tea.KeyEnd})
		last := m.View()
		if first == last || !strings.Contains(ansi.Strip(last), "Ctrl+Q") {
			t.Fatalf("cannot reach end of help at %v", size)
		}
		for _, view := range []string{first, last} {
			if n := len(strings.Split(view, "\n")); n != size[1] {
				t.Fatalf("%v: %d screen lines", size, n)
			}
			for _, line := range strings.Split(view, "\n") {
				if ansi.StringWidth(line) > size[0] {
					t.Fatalf("%v: overflow %q", size, line)
				}
			}
		}
	}
}

func TestLifecycleEntryPointsConfirmTheSameEffect(t *testing.T) {
	m := uxModel(t)
	id := m.st.Sessions[0].ID
	m.live[state.TmuxName(id)] = tmuxctl.Info{}
	m.keyNormalStr("alt+z")
	keyboard := m.confirmMsg
	if m.mode != modeConfirm || !strings.Contains(keyboard, "Stops the agent process") {
		t.Fatal("keyboard stop lacks consequences")
	}
	m.mode = modeNormal
	m.menuRow = row{rowSession, id}
	m.execMenu("sleep")
	if m.mode != modeConfirm || m.confirmMsg != keyboard {
		t.Fatal("context stop bypasses the shared confirmation")
	}
	if !m.isLive(id) {
		t.Fatal("stop ran before confirmation")
	}
	m.mode = modeNormal
	m.execMenu("delete")
	if !strings.Contains(m.confirmMsg, "transcripts and project files remain") {
		t.Fatal("removal scope is misleading")
	}
}

func TestAttentionFiltersAndSelection(t *testing.T) {
	m := uxModel(t)
	for i, s := range m.st.Sessions {
		m.live[state.TmuxName(s.ID)] = tmuxctl.Info{}
		m.runtime[s.ID] = status.Runtime{Status: status.NeedsYou, UpdatedAt: time.Now().Add(time.Duration(i) * time.Minute)}
	}
	m.st.Sessions[1].Agent = state.AgentCodex
	m.st.Groups[0].Collapsed = true
	m.attentionOnly = true
	m.buildRows()
	if len(m.rows) != 3 || m.rows[1].kind != rowSession {
		t.Fatal("attention queue hid folded sessions or retained headers")
	}
	m.toggleMarked()
	first := m.selectedSessionID()
	m.filter = "agent:codex status:blocked"
	m.buildRows()
	if len(m.rows) != 1 || m.rows[0].id != m.st.Sessions[1].ID {
		t.Fatal("combined filters returned the wrong session")
	}
	if !m.marked[first] {
		t.Fatal("filter lost selection")
	}
	m.selectVisible()
	if len(m.actionTargets()) != 2 {
		t.Fatal("batch targets lost sessions hidden by filters")
	}
	m.keyNormalStr("esc")
	if len(m.marked) != 0 || !m.filtersActive() {
		t.Fatal("Escape did not clear selection before filters")
	}
	m.keyNormalStr("esc")
	if m.filtersActive() {
		t.Fatal("Escape did not clear filters")
	}
}

func TestAttentionRowsKeepMouseAndScrollAligned(t *testing.T) {
	m := uxModel(t)
	m.attentionOnly = true
	for i := 0; i < 20; i++ {
		id := m.st.AddSession("blocked", "/tmp", "")
		m.live[state.TmuxName(id)] = tmuxctl.Info{}
		m.runtime[id] = status.Runtime{Status: status.NeedsYou}
	}
	m.buildRows()
	m.sel = len(m.rows) - 1
	m.ensureVisible()
	if m.top == 0 {
		t.Fatal("selected row did not scroll into view")
	}
	for y := 0; y < m.listRowsHeight(); y += 2 {
		idx := m.rowAt(m.listTopY() + y)
		if idx >= len(m.rows) {
			break
		}
		if idx != m.rowAt(m.listTopY()+y+1) {
			t.Fatal("reason line targets a different session")
		}
	}
	if m.rowAt(m.listTopY()+m.listRowsHeight()) != -1 {
		t.Fatal("details panel is clickable as a session")
	}
}

func TestCommandPaletteAndSafeBareLetters(t *testing.T) {
	m := uxModel(t)
	m.keyNormalStr("p")
	if m.mode != modeNormal {
		t.Fatal("bare p opened commands")
	}
	m.keyNormalStr("alt+p")
	m.keyCommands(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("project")})
	items := m.commandItems()
	for i, it := range items {
		if it.id == "filter-project" {
			m.pickSel = i
			break
		}
	}
	m.keyCommands(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeGroupPick || m.pickKind != "filter-project" {
		t.Fatal("palette command did not open the project filter")
	}
}

func TestNamesUseEmptyMetricSpaceAndTabNumbersStayStable(t *testing.T) {
	m := uxModel(t)
	m.st.Sessions[0].Name = "payments-idempotency-refactor"
	for _, s := range m.st.Sessions {
		m.live[state.TmuxName(s.ID)] = tmuxctl.Info{}
	}
	if !strings.Contains(ansi.Strip(m.renderSessionRow(m.st.Sessions[0].ID, 1, 46)), m.st.Sessions[0].Name) {
		t.Fatal("empty metric columns truncated the name")
	}
	m.filter = "gamma"
	m.buildRows()
	if m.nthSession(3) < 0 || m.nthSession(1) != -1 {
		t.Fatal("filter renumbered the tabs")
	}
}

func TestRowMetricsStayAligned(t *testing.T) {
	for _, width := range []int{65, 67, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := uxModel(t)
			m.st.ShowMetrics = true
			cases := []struct {
				family, agent, context, cost string
				tokens                       int
				total                        float64
				kind                         status.Kind
			}{
				{"opus", state.AgentClaude, "~9%", "$0.42", 90_000, .42, status.Working},
				{"sonnet", state.AgentClaude, "~70%", "$12.3", 700_000, 12.3, status.NeedsYou},
				{"haiku", state.AgentClaude, "~100%", "$128", 1_000_000, 128, status.Attention},
				{"gpt-5.6", state.AgentCodex, "85%", "", 850_000, 0, status.Idle},
				{"opus", state.AgentClaude, "~90%", "$99999", 900_000, 99999, status.Dormant},
				{"", state.AgentClaude, "", "", 0, 0, status.Idle},
			}
			modelStart, contextEnd, costEnd := -1, -1, -1
			for i, tc := range cases {
				group := ""
				if i%2 == 1 {
					group = m.st.Groups[0].ID
				}
				id := m.st.AddSession("任务 payments refactor", "/tmp/very-long-project-name-that-must-not-shift-the-metrics", group)
				m.st.Session(id).Agent = tc.agent
				m.nameProbes[id] = &nameProbe{
					info: agents.Info{Family: tc.family, ContextTokens: tc.tokens, ContextWindow: 1_000_000},
					cost: agents.CostState{Total: tc.total},
				}
				if tc.kind != status.Dormant {
					m.live[state.TmuxName(id)] = tmuxctl.Info{}
				}
				m.runtime[id] = status.Runtime{Status: tc.kind, UpdatedAt: time.Now()}
				line := ansi.Strip(m.renderSessionRow(id, i+1, width))
				if got := ansi.StringWidth(line); got != width {
					t.Fatalf("%s: row width = %d, want %d: %q", tc.kind, got, width, line)
				}
				if strings.Contains(line, "ctx ") || strings.Contains(line, "est ") {
					t.Fatalf("redundant row labels: %q", line)
				}
				if tc.family != "" {
					at := strings.Index(line, tc.family)
					if at < 0 {
						t.Fatalf("missing model %q: %q", tc.family, line)
					}
					start := ansi.StringWidth(line[:at])
					if modelStart < 0 {
						modelStart = start
					} else if start != modelStart {
						t.Fatalf("model moved from column %d to %d: %q", modelStart, start, line)
					}
				}
				for _, metric := range []struct {
					value string
					end   *int
				}{{tc.context, &contextEnd}, {tc.cost, &costEnd}} {
					if metric.value == "" {
						continue
					}
					at := strings.Index(line, metric.value)
					if at < 0 {
						t.Fatalf("missing metric %q: %q", metric.value, line)
					}
					end := ansi.StringWidth(line[:at+len(metric.value)])
					if *metric.end < 0 {
						*metric.end = end
					} else if end != *metric.end {
						t.Fatalf("%s moved from column %d to %d: %q", metric.value, *metric.end, end, line)
					}
				}
				if tc.cost == "" && strings.Contains(line, "$") {
					t.Fatalf("unknown cost rendered as a price: %q", line)
				}
				if tc.context == "" && strings.Contains(line, "%") {
					t.Fatalf("unknown context rendered as a percentage: %q", line)
				}
			}
		})
	}
}

func TestGlobalActionsRevealTheirActualTarget(t *testing.T) {
	m := uxModel(t)
	target := m.st.Sessions[1].ID
	m.st.Groups[0].Collapsed = true
	m.filter = "alpha"
	m.buildRows()
	m.revealSession(target)
	if m.selectedSessionID() != target || m.filtersActive() || m.st.Groups[0].Collapsed {
		t.Fatal("global action would target a different visible row")
	}
}
