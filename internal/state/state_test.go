package state

import (
	"path/filepath"
	"testing"
)

func desk(t *testing.T) *State {
	t.Helper()
	s := &State{Version: 1}
	gTools := s.AddGroup("tools")
	gRev := s.AddGroup("reviews")
	// layout: [u1 u2] tools:[t1 t2] reviews:[m1]
	s.AddSession("u1", "/tmp", "")
	s.AddSession("u2", "/tmp", "")
	s.AddSession("t1", "/tmp", gTools)
	s.AddSession("t2", "/tmp", gTools)
	s.AddSession("m1", "/tmp", gRev)
	return s
}

func names(s *State) []string {
	var out []string
	for _, x := range s.Sessions {
		out = append(out, x.Name)
	}
	return out
}

func byName(s *State, name string) *Session {
	for i := range s.Sessions {
		if s.Sessions[i].Name == name {
			return &s.Sessions[i]
		}
	}
	return nil
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestAddSessionInsertsAtGroupEnd(t *testing.T) {
	s := desk(t)
	gTools := s.Groups[0].ID
	s.AddSession("t3", "/tmp", gTools)
	eq(t, names(s), []string{"u1", "u2", "t1", "t2", "t3", "m1"})
}

func TestMoveWithinGroup(t *testing.T) {
	s := desk(t)
	s.MoveSession(byName(s, "t2").ID, -1)
	eq(t, names(s), []string{"u1", "u2", "t2", "t1", "m1"})
	s.MoveSession(byName(s, "t2").ID, 1)
	eq(t, names(s), []string{"u1", "u2", "t1", "t2", "m1"})
}

func TestMoveAcrossGroupBoundaryDown(t *testing.T) {
	s := desk(t)
	// u2 is last ungrouped; moving down enters "tools" as first member
	s.MoveSession(byName(s, "u2").ID, 1)
	eq(t, names(s), []string{"u1", "u2", "t1", "t2", "m1"})
	if byName(s, "u2").GroupID != s.Groups[0].ID {
		t.Fatal("u2 should now be in tools")
	}
	// keep going: through tools into reviews
	s.MoveSession(byName(s, "u2").ID, 1) // swap with t1
	s.MoveSession(byName(s, "u2").ID, 1) // swap with t2
	s.MoveSession(byName(s, "u2").ID, 1) // cross into reviews
	if byName(s, "u2").GroupID != s.Groups[1].ID {
		t.Fatal("u2 should now be in reviews")
	}
	eq(t, names(s), []string{"u1", "t1", "t2", "u2", "m1"})
}

func TestMoveAcrossGroupBoundaryUp(t *testing.T) {
	s := desk(t)
	// t1 is first in tools; moving up leaves the group into ungrouped (last)
	s.MoveSession(byName(s, "t1").ID, -1)
	if byName(s, "t1").GroupID != "" {
		t.Fatal("t1 should be ungrouped")
	}
	eq(t, names(s), []string{"u1", "u2", "t1", "t2", "m1"})
}

func TestMoveAtEdgesNoop(t *testing.T) {
	s := desk(t)
	s.MoveSession(byName(s, "u1").ID, -1) // top of desk
	eq(t, names(s), []string{"u1", "u2", "t1", "t2", "m1"})
	s.MoveSession(byName(s, "m1").ID, 1) // bottom of desk
	eq(t, names(s), []string{"u1", "u2", "t1", "t2", "m1"})
}

func TestMoveToGroup(t *testing.T) {
	s := desk(t)
	gRev := s.Groups[1].ID
	s.MoveToGroup(byName(s, "u1").ID, gRev)
	eq(t, names(s), []string{"u2", "t1", "t2", "m1", "u1"})
	if byName(s, "u1").GroupID != gRev {
		t.Fatal("u1 should be in reviews")
	}
	// moving to same group is a no-op
	s.MoveToGroup(byName(s, "u1").ID, gRev)
	eq(t, names(s), []string{"u2", "t1", "t2", "m1", "u1"})
}

func TestDeleteGroupUngroupsSessions(t *testing.T) {
	s := desk(t)
	s.DeleteGroup(s.Groups[0].ID)
	if len(s.Groups) != 1 {
		t.Fatal("group not removed")
	}
	if byName(s, "t1").GroupID != "" || byName(s, "t2").GroupID != "" {
		t.Fatal("sessions should be ungrouped")
	}
}

func TestMoveGroup(t *testing.T) {
	s := desk(t)
	s.MoveGroup(s.Groups[1].ID, -1)
	if s.Groups[0].Name != "reviews" {
		t.Fatal("reviews should be first")
	}
	s.MoveGroup(s.Groups[0].ID, -1) // edge: no-op
	if s.Groups[0].Name != "reviews" {
		t.Fatal("edge move should be a no-op")
	}
}

func TestMoveSessionNextTo(t *testing.T) {
	s := desk(t) // [u1 u2] tools:[t1 t2] reviews:[m1]
	// drag u1 right after t1: adopts tools, sits between t1 and t2
	s.MoveSessionNextTo(byName(s, "u1").ID, byName(s, "t1").ID, true)
	eq(t, names(s), []string{"u2", "t1", "u1", "t2", "m1"})
	if byName(s, "u1").GroupID != s.Groups[0].ID {
		t.Fatal("u1 should be in tools")
	}
	// drag m1 before u2 (into ungrouped)
	s.MoveSessionNextTo(byName(s, "m1").ID, byName(s, "u2").ID, false)
	eq(t, names(s), []string{"m1", "u2", "t1", "u1", "t2"})
	if byName(s, "m1").GroupID != "" {
		t.Fatal("m1 should be ungrouped")
	}
	// self / missing targets are no-ops
	s.MoveSessionNextTo(byName(s, "m1").ID, byName(s, "m1").ID, true)
	s.MoveSessionNextTo(byName(s, "m1").ID, "nope", true)
	eq(t, names(s), []string{"m1", "u2", "t1", "u1", "t2"})
}

func TestMoveToGroupFront(t *testing.T) {
	s := desk(t)
	s.MoveToGroupFront(byName(s, "m1").ID, s.Groups[0].ID) // reviews' m1 -> first of tools
	eq(t, names(s), []string{"u1", "u2", "m1", "t1", "t2"})
	if byName(s, "m1").GroupID != s.Groups[0].ID {
		t.Fatal("m1 should be in tools")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := desk(t)
	path := filepath.Join(t.TempDir(), "state.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(got), names(s))
	if len(got.Groups) != 2 || got.Groups[0].Name != "tools" {
		t.Fatalf("groups lost: %+v", got.Groups)
	}
}

func TestLoadMissingFile(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || s == nil || len(s.Sessions) != 0 {
		t.Fatalf("missing file should give empty state, got %v %v", s, err)
	}
}
