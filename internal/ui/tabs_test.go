package ui

import "testing"

// The tab strip must never hide the active tab: whatever the scroll position
// was, the window slides until the active tab is inside it.
func TestTabWindowKeepsActiveVisible(t *testing.T) {
	widths := []int{10, 10, 10, 10, 10, 10, 10, 10} // 80 cells of tabs

	// Everything fits: no windowing, scroll resets.
	if s, e := tabWindow(widths, 7, 3, 100); s != 0 || e != 8 {
		t.Errorf("all-fit: got [%d,%d), want [0,8)", s, e)
	}

	// Unknown width (not measured yet): show everything.
	if s, e := tabWindow(widths, 7, 0, 0); s != 0 || e != 8 {
		t.Errorf("unmeasured: got [%d,%d), want [0,8)", s, e)
	}

	// 45 cells: active at the far right forces the window over to it.
	s, e := tabWindow(widths, 7, 0, 45)
	if 7 < s || 7 >= e {
		t.Errorf("active 7 not inside [%d,%d)", s, e)
	}
	// And tabs are hidden on the left, so the left chip's width was budgeted:
	// visible tabs plus the chip must fit in 45.
	used := chipWidth(s)
	for _, w := range widths[s:e] {
		used += w
	}
	if used > 45 {
		t.Errorf("window [%d,%d) plus chip = %d cells, over the 45 budget", s, e, used)
	}

	// Active at the far left snaps the window back.
	if s, e = tabWindow(widths, 0, 5, 45); s != 0 {
		t.Errorf("active 0: window starts at %d, want 0", s)
	} else if e >= 8 {
		t.Errorf("active 0 in 45 cells: nothing hidden right? [%d,%d)", s, e)
	}

	// The previous scroll is kept when the active tab is already visible.
	if s, _ = tabWindow(widths, 3, 2, 45); s != 2 {
		t.Errorf("sticky scroll: got start %d, want 2", s)
	}

	// A window too small for even one tab still shows the active one.
	if s, e = tabWindow(widths, 4, 0, 5); s > 4 || e <= 4 {
		t.Errorf("tiny window: active 4 not in [%d,%d)", s, e)
	}

	// No tabs at all.
	if s, e = tabWindow(nil, -1, 0, 45); s != 0 || e != 0 {
		t.Errorf("empty: got [%d,%d)", s, e)
	}
}
