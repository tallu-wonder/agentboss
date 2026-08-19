package ui

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/tallu-wonder/agentboss/internal/status"
)

// rowKind distinguishes list rows.
type rowKind int

const (
	rowGroup rowKind = iota
	rowSession
)

// row is one visible line in the session list.
type row struct {
	kind rowKind
	id   string // session ID or group ID ("" = the implicit ungrouped header)
}

// oldSection is the pseudo-group ID of the archived-sessions section.
const oldSection = "@old"

// buildRows flattens groups+sessions into the visible list, honoring
// collapse state, the sort mode, and the active search filter. It tries to
// keep the same item selected across rebuilds.
func (m *Model) buildRows() {
	var prevID string
	var prevKind rowKind
	if m.sel >= 0 && m.sel < len(m.rows) {
		prevID = m.rows[m.sel].id
		prevKind = m.rows[m.sel].kind
	}

	filter := strings.TrimSpace(m.filter)
	m.rows = m.rows[:0]

	groupName := map[string]string{}
	for _, g := range m.st.Groups {
		groupName[g.ID] = g.Name
	}

	appendGroup := func(gid string) {
		members := m.membersOf(gid)
		var matched []string
		for _, sid := range members {
			s := m.st.Session(sid)
			if filter == "" || fuzzyMatch(filter, s.Name, s.Dir+" "+groupName[gid]) {
				matched = append(matched, sid)
			}
		}
		if gid == "" {
			// Ungrouped sessions get no header; they sit at the top.
			for _, sid := range matched {
				m.rows = append(m.rows, row{rowSession, sid})
			}
			return
		}
		g := m.st.Group(gid)
		if filter != "" && len(matched) == 0 {
			return // hide empty groups while searching
		}
		m.rows = append(m.rows, row{rowGroup, gid})
		if g.Collapsed && filter == "" {
			return
		}
		for _, sid := range matched {
			m.rows = append(m.rows, row{rowSession, sid})
		}
	}

	appendGroup("")
	for _, g := range m.st.Groups {
		appendGroup(g.ID)
	}

	// Archived sessions live in the "old" section at the bottom.
	old := m.archivedIDs()
	if len(old) > 0 {
		var matched []string
		for _, sid := range old {
			s := m.st.Session(sid)
			if filter == "" || fuzzyMatch(filter, s.Name, s.Dir+" old") {
				matched = append(matched, sid)
			}
		}
		if filter == "" || len(matched) > 0 {
			m.rows = append(m.rows, row{rowGroup, oldSection})
			if m.st.OldExpanded || filter != "" {
				for _, sid := range matched {
					m.rows = append(m.rows, row{rowSession, sid})
				}
			}
		}
	}

	// Restore selection on the same item if it is still visible.
	m.sel = -1
	for i, r := range m.rows {
		if r.id == prevID && r.kind == prevKind {
			m.sel = i
			break
		}
	}
	if m.sel == -1 {
		m.sel = m.firstSessionRow()
	}
	m.clampSel()
	if m.top >= len(m.rows) {
		m.top = 0
	}
	m.ensureVisible()
}

// membersOf returns the non-archived sessions of a group, ordered per the
// current sort mode (manual = desk order).
func (m *Model) membersOf(gid string) []string {
	var out []string
	for i := range m.st.Sessions {
		s := &m.st.Sessions[i]
		if s.GroupID == gid && !s.Archived {
			out = append(out, s.ID)
		}
	}
	m.sortIDs(out)
	return out
}

// archivedIDs returns archived sessions, most recently touched first.
func (m *Model) archivedIDs() []string {
	var out []string
	for i := range m.st.Sessions {
		if m.st.Sessions[i].Archived {
			out = append(out, m.st.Sessions[i].ID)
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		return m.lastActivity(out[a]).After(m.lastActivity(out[b]))
	})
	return out
}

// lastActivity is the newest of a session's hook event and open times.
func (m *Model) lastActivity(id string) time.Time {
	t := m.runtime[id].UpdatedAt
	if s := m.st.Session(id); s != nil && s.LastOpenedAt.After(t) {
		t = s.LastOpenedAt
	}
	return t
}

func statusRank(k status.Kind) int {
	switch k {
	case status.NeedsYou:
		return 0
	case status.Attention:
		return 1
	case status.Working:
		return 2
	case status.Idle:
		return 3
	default:
		return 4
	}
}

// sortIDs orders session IDs per the desk's sort mode; manual keeps the
// caller's (desk) order.
func (m *Model) sortIDs(ids []string) {
	// The comparators run over ids the caller collected, so a session could in
	// principle have gone by the time they are compared; sort.Slice must never
	// panic, or it takes the manager down with it.
	name := func(id string) string {
		if s := m.st.Session(id); s != nil {
			return strings.ToLower(s.Name)
		}
		return ""
	}
	switch m.st.SortMode {
	case "name":
		sort.SliceStable(ids, func(a, b int) bool {
			return name(ids[a]) < name(ids[b])
		})
	case "folder":
		sort.SliceStable(ids, func(a, b int) bool {
			sa, sb := m.st.Session(ids[a]), m.st.Session(ids[b])
			if sa == nil || sb == nil {
				return sb == nil && sa != nil
			}
			if sa.Dir != sb.Dir {
				return sa.Dir < sb.Dir
			}
			return strings.ToLower(sa.Name) < strings.ToLower(sb.Name)
		})
	case "recent":
		sort.SliceStable(ids, func(a, b int) bool {
			return m.lastActivity(ids[a]).After(m.lastActivity(ids[b]))
		})
	case "status":
		sort.SliceStable(ids, func(a, b int) bool {
			ra, rb := statusRank(m.statusOf(ids[a])), statusRank(m.statusOf(ids[b]))
			if ra != rb {
				return ra < rb
			}
			return m.lastActivity(ids[a]).After(m.lastActivity(ids[b]))
		})
	}
}

// orderedLive returns live sessions in SIDEBAR order — ungrouped first,
// then each group's members in group order — so the tab bar always mirrors
// the left panel exactly.
func (m *Model) orderedLive() []string {
	var out []string
	add := func(gid string) {
		for _, sid := range m.membersOf(gid) { // already sorted per mode
			if m.isLive(sid) {
				out = append(out, sid)
			}
		}
	}
	add("")
	for _, g := range m.st.Groups {
		add(g.ID)
	}
	return out
}

// firstSessionRow returns the index of the first session row (or 0).
func (m *Model) firstSessionRow() int {
	for i, r := range m.rows {
		if r.kind == rowSession {
			return i
		}
	}
	return 0
}

func (m *Model) clampSel() {
	if len(m.rows) == 0 {
		m.sel = 0
		m.top = 0
		return
	}
	if m.sel < 0 {
		m.sel = 0
	}
	if m.sel >= len(m.rows) {
		m.sel = len(m.rows) - 1
	}
}

// selectedRow returns the currently selected row, or nil.
func (m *Model) selectedRow() *row {
	if m.sel < 0 || m.sel >= len(m.rows) {
		return nil
	}
	return &m.rows[m.sel]
}

// selectedSessionID returns the selected session's ID, or "".
func (m *Model) selectedSessionID() string {
	r := m.selectedRow()
	if r == nil || r.kind != rowSession {
		return ""
	}
	return r.id
}

// nthSession returns the row index of the n-th (1-based) OPEN session — the
// same n the digit keys and the number badges use.
//
// Counting open sessions rather than every row makes the digits mean what the
// tab bar shows: 1 is the leftmost tab. Numbering every row instead put digits
// on dormant sessions, so the shortcuts drifted out of step with the tabs as
// soon as anything was asleep, and pressing a digit could wake a session you
// were not thinking about.
func (m *Model) nthSession(n int) int {
	count := 0
	for i, r := range m.rows {
		if r.kind == rowSession && m.isLive(r.id) {
			count++
			if count == n {
				return i
			}
		}
	}
	return -1
}

// nextAlert returns the row index of the next session (after the selection,
// cycling) that needs attention, or -1. Alerts inside collapsed groups are
// reachable: the group is expanded so the alert can never stay buried.
func (m *Model) nextAlert() int {
	// Walk the full session list in desk order, starting after the selection.
	cur := m.selectedSessionID()
	start := 0
	for i := range m.st.Sessions {
		if m.st.Sessions[i].ID == cur {
			start = i + 1
			break
		}
	}
	n := len(m.st.Sessions)
	for off := 0; off < n; off++ {
		s := &m.st.Sessions[(start+off)%n]
		k := m.statusOf(s.ID)
		if k != alertNeedsYou && k != alertAttention {
			continue
		}
		if g := m.st.Group(s.GroupID); g != nil && g.Collapsed {
			g.Collapsed = false
			m.dirty = true
			m.buildRows()
		}
		for i, r := range m.rows {
			if r.kind == rowSession && r.id == s.ID {
				return i
			}
		}
	}
	return -1
}

// fuzzyMatch reports whether every space-separated term of the query matches
// the session. A term matches as a substring of name/dir/group (so paths are
// searchable precisely), or as a subsequence of the session NAME only (so
// "agr" finds "API gateway retries"). Pure subsequence over long paths is not
// used — it matches nearly everything.
func fuzzyMatch(query, name, rest string) bool {
	lname := strings.ToLower(name)
	lfull := lname + " " + strings.ToLower(rest)
	for _, term := range strings.FieldsFunc(strings.ToLower(query), unicode.IsSpace) {
		if !strings.Contains(lfull, term) && !subsequence(term, lname) {
			return false
		}
	}
	return true
}

// subsequence reports whether the runes of q appear in order within s.
func subsequence(q, s string) bool {
	rs := []rune(s)
	ti := 0
	for _, c := range q {
		found := false
		for ; ti < len(rs); ti++ {
			if rs[ti] == c {
				found = true
				ti++
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
