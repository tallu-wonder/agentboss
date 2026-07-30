package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/tallu-wonder/agentdeck/internal/state"
	"github.com/tallu-wonder/agentdeck/internal/status"
	"github.com/tallu-wonder/agentdeck/internal/tmuxctl"
)

func ansiWidth(s string) int { return ansi.StringWidth(s) }

func TestFuzzyMatch(t *testing.T) {
	cases := []struct {
		q, name, rest string
		want          bool
	}{
		{"gate", "API gateway retries", "", true},
		{"agr", "API gateway retries", "", true}, // subsequence over the name
		{"retries api", "API gateway retries", "", true},
		{"xyz", "API gateway retries", "", false},
		{"", "anything", "", true},
		{"docs", "sess", "/home/dev/projects/docs-site", true}, // substring over dir
		{"docs sweep", "docs sweep", "/srv/docs-site", true},
		// subsequence must NOT apply to paths: "api" is a subsequence of this
		// path but not a substring of it, and not related to the name.
		{"api", "daily helper", "/home/dev/projects/alpha-pipeline", false},
	}
	for _, c := range cases {
		if got := fuzzyMatch(c.q, c.name, c.rest); got != c.want {
			t.Errorf("fuzzyMatch(%q, %q, %q) = %v, want %v", c.q, c.name, c.rest, got, c.want)
		}
	}
}

func testModel() *Model {
	st := &state.State{Version: 1}
	g := st.AddGroup("tools")
	st.AddSession("alpha", "/tmp", "")
	st.AddSession("beta", "/tmp", g)
	st.AddSession("gamma", "/tmp", g)
	m := &Model{st: st}
	m.buildRows()
	return m
}

func TestBuildRowsLayout(t *testing.T) {
	m := testModel()
	// expect: alpha(session), tools(group), beta, gamma
	if len(m.rows) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(m.rows))
	}
	if m.rows[0].kind != rowSession || m.rows[1].kind != rowGroup ||
		m.rows[2].kind != rowSession || m.rows[3].kind != rowSession {
		t.Fatalf("unexpected row layout: %+v", m.rows)
	}
}

func TestBuildRowsCollapse(t *testing.T) {
	m := testModel()
	m.st.Groups[0].Collapsed = true
	m.buildRows()
	if len(m.rows) != 2 { // alpha + collapsed header
		t.Fatalf("collapsed group should hide members, got %d rows", len(m.rows))
	}
}

func TestBuildRowsFilterOverridesCollapse(t *testing.T) {
	m := testModel()
	m.st.Groups[0].Collapsed = true
	m.filter = "beta"
	m.buildRows()
	// searching must see inside collapsed groups: header + beta
	if len(m.rows) != 2 || m.rows[1].kind != rowSession {
		t.Fatalf("filter should reveal collapsed members, rows=%+v", m.rows)
	}
	if m.rows[1].id != m.st.Sessions[1].ID {
		t.Fatal("wrong session matched")
	}
}

func TestBuildRowsFilterHidesEmptyGroups(t *testing.T) {
	m := testModel()
	m.filter = "alpha"
	m.buildRows()
	if len(m.rows) != 1 || m.rows[0].kind != rowSession {
		t.Fatalf("empty groups should be hidden while filtering, rows=%+v", m.rows)
	}
}

func TestSelectionSurvivesRebuild(t *testing.T) {
	m := testModel()
	m.sel = 3 // gamma
	id := m.rows[3].id
	m.buildRows()
	if m.rows[m.sel].id != id {
		t.Fatal("selection should stick to the same session across rebuilds")
	}
}

// The digit keys address OPEN sessions, so they line up with the tab bar. A
// dormant session in between must not consume a number.
func TestNthSessionCountsOnlyOpenSessions(t *testing.T) {
	m := testModel()
	// rows: 0 alpha(session), 1 tools(group), 2 beta, 3 gamma
	live := func(row int) { m.live[state.TmuxName(m.rows[row].id)] = tmuxctl.Info{} }
	m.live = map[string]tmuxctl.Info{}
	live(0) // alpha
	live(3) // gamma — beta stays dormant

	if idx := m.nthSession(1); idx != 0 {
		t.Errorf("nthSession(1) = %d, want row 0 (alpha)", idx)
	}
	if idx := m.nthSession(2); idx != 3 {
		t.Errorf("nthSession(2) = %d, want row 3 (gamma — beta is dormant and skipped)", idx)
	}
	if idx := m.nthSession(3); idx != -1 {
		t.Errorf("nthSession(3) = %d, want -1: only two sessions are open", idx)
	}
	if idx := m.nthSession(9); idx != -1 {
		t.Error("out-of-range session number should return -1")
	}

	// With nothing open, no digit addresses anything.
	m.live = map[string]tmuxctl.Info{}
	if idx := m.nthSession(1); idx != -1 {
		t.Errorf("nothing open: nthSession(1) = %d, want -1", idx)
	}
}

func TestNextAlertExpandsCollapsedGroup(t *testing.T) {
	m := testModel()
	m.st.Groups[0].Collapsed = true
	// gamma (inside the collapsed group) has an alert; fake it as live.
	gamma := m.st.Sessions[2]
	m.live = map[string]tmuxctl.Info{state.TmuxName(gamma.ID): {Name: state.TmuxName(gamma.ID)}}
	m.runtime = map[string]status.Runtime{gamma.ID: {Status: status.Attention}}
	m.buildRows()

	idx := m.nextAlert()
	if idx < 0 {
		t.Fatal("alert in collapsed group must be reachable")
	}
	if m.rows[idx].id != gamma.ID {
		t.Fatalf("wrong row selected: %+v", m.rows[idx])
	}
	if m.st.Groups[0].Collapsed {
		t.Fatal("group should have been expanded to reveal the alert")
	}
}

func TestPad(t *testing.T) {
	if got := pad("hi", 5); got != "hi   " {
		t.Fatalf("pad short: %q", got)
	}
	if got := pad("hello world", 5); ansiWidth(got) != 5 {
		t.Fatalf("pad truncate width: %q", got)
	}
	colored := "\x1b[31mred\x1b[0m"
	if got := pad(colored, 6); ansiWidth(got) != 6 {
		t.Fatalf("pad ansi width: %q", got)
	}
	if pad("x", 0) != "" || pad("x", -1) != "" {
		t.Fatal("non-positive widths must yield empty string")
	}
}

func TestColumnFormattersFitTheirColumns(t *testing.T) {
	// Every value must fit its column, or padNum truncates and drops the unit
	// (a 5-char "13.4M" in a 4-cell column once rendered as "13.4").
	for _, n := range []int{0, 1, 999, 1_000, 87_432, 781_000, 999_999,
		1_000_000, 1_240_000, 9_900_000, 13_400_000, 250_000_000} {
		if got := fmtTokens(n); ansiWidth(got) > tokenW {
			t.Errorf("fmtTokens(%d) = %q, %d cells > column %d", n, got, ansiWidth(got), tokenW)
		}
	}
	for _, v := range []float64{0.01, 5.85, 29.7, 99.99, 108, 737, 1600, 99999} {
		if got := fmtUSD(v); ansiWidth(got) > costW {
			t.Errorf("fmtUSD(%v) = %q, %d cells > column %d", v, got, ansiWidth(got), costW)
		}
	}
	for _, f := range []string{"opus", "fable", "sonnet", "haiku", "gpt-5.6"} {
		if ansiWidth(f) > modelW {
			t.Errorf("family %q is %d cells > column %d", f, ansiWidth(f), modelW)
		}
	}
	// padNum right-aligns and never changes the cell width.
	for _, w := range []int{3, 4, 6} {
		for _, s := range []string{"", "1", "781k", "$1600", "toolongvalue"} {
			if got := ansiWidth(padNum(s, w)); got != w && s != "" {
				t.Errorf("padNum(%q, %d) is %d cells, want %d", s, w, got, w)
			}
		}
	}
}

// A model badge is scraped from the agent's status line. It must not be fooled
// by a conversation that talks about models — that showed an Opus session as
// "sonnet".
func TestPaneModelReNeedsAVersionedChip(t *testing.T) {
	// The version proves it is a status chip; the BADGE is the family alone, so
	// the column stays one narrow word ("opus", never "opus 4.8").
	chips := map[string]string{
		"▎ Using Opus 5 (1M context) · /model":  "opus",
		"  Sonnet 4.8 · 42k tokens":             "sonnet",
		"model: gpt-5.6-sol   98% context left": "gpt-5.6-sol",
		"  fable-5  ":                           "fable",
	}
	for line, want := range chips {
		mm := paneModelRe.FindAllStringSubmatch(line, -1)
		if len(mm) == 0 {
			t.Errorf("%q: no chip found, want %q", line, want)
			continue
		}
		last := mm[len(mm)-1]
		fam := last[1]
		if fam == "" {
			fam = last[3]
		}
		if strings.ToLower(fam) != want {
			t.Errorf("%q: family = %q, want %q", line, fam, want)
		}
	}
	for _, prose := range []string{
		"we should compare sonnet and opus for this",
		"the haiku model is cheaper",
		"Opus wrote that, not Sonnet",
		"switch to codex?",
	} {
		if got := paneModelRe.FindAllString(prose, -1); len(got) > 0 {
			// "codex" alone is a legitimate Codex chip, so allow that one.
			if prose != "switch to codex?" {
				t.Errorf("prose %q matched %v — a badge would be wrong", prose, got)
			}
		}
	}
}
