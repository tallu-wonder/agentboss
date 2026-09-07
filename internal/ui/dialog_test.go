package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/tallu-wonder/agentboss/internal/state"
	"github.com/tallu-wonder/agentboss/internal/tmuxctl"
)

func TestQueuedConfirmationKeepsItsSourceSession(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(fmt.Sprint(removed), func(t *testing.T) {
			m := uxModel(t)
			origin, other := m.st.Sessions[0], m.st.Sessions[1]
			m.activeID = other.ID // a later tab switch must not redirect the action
			m.live[state.TmuxName(origin.ID)] = tmuxctl.Info{}
			m.live[state.TmuxName(other.ID)] = tmuxctl.Info{}
			if removed {
				m.st.Sessions = m.st.Sessions[1:]
				m.buildRows()
			}
			dir := t.TempDir()
			t.Setenv("AGENTBOSS_HOME", dir)
			if err := os.Mkdir(filepath.Join(dir, "cmd"), 0700); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(map[string]any{"op": "act", "to": "z", "origin": tmuxctl.PopupOrigin{Session: state.TmuxName(origin.ID)}})
			if err := os.WriteFile(filepath.Join(dir, "cmd", "action.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			m.drainCmds()
			if removed {
				if m.mode != modeNormal || m.confirmFn != nil {
					t.Fatal("a removed source session redirected the confirmation")
				}
			} else if m.mode != modeConfirm || !strings.Contains(m.confirmMsg, origin.Name) || strings.Contains(m.confirmMsg, other.Name) {
				t.Fatalf("confirmation did not preserve the source session: %q", m.confirmMsg)
			}
		})
	}
}

func TestPaneDialogRequiresExplicitConfirmation(t *testing.T) {
	for _, key := range []string{"n", "N", "q", "enter", "esc", "ctrl+c", "y", "Y"} {
		m := &paneDialog{request: dialogRequest{Confirm: true}}
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		switch key {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "ctrl+c":
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
		}
		m.Update(msg)
		if m.approved != (key == "y" || key == "Y") {
			t.Errorf("key %q: approved=%v", key, m.approved)
		}
	}
	m := &paneDialog{}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if m.approved {
		t.Fatal("a help/info dialog produced an approval")
	}
}

func TestPaneDialogScrollKeepsAnswerKeysVisible(t *testing.T) {
	for _, size := range [][2]int{{24, 8}, {64, 20}} {
		m := &paneDialog{width: size[0], height: size[1], request: dialogRequest{Confirm: true}}
		for i := 0; i < 100; i++ {
			m.request.Lines = append(m.request.Lines, fmt.Sprintf("session with a long descriptive name %03d", i))
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEnd})
		view := m.View()
		plain := ansi.Strip(view)
		if !strings.Contains(plain, "099") || !strings.Contains(plain, "confirm") || !strings.Contains(plain, "cancel") {
			t.Fatalf("cannot review the final target and answer at %v: %s", size, plain)
		}
		lines := strings.Split(view, "\n")
		if len(lines) > size[1] {
			t.Fatalf("dialog overflows height at %v", size)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("dialog overflows width at %v: %q", size, line)
			}
		}
	}
}

// Locate the visible label, independently of the layout's hit regions.
func confirmationButtonCells(t *testing.T, view, label string) (int, int, int) {
	t.Helper()
	lines := strings.Split(ansi.Strip(view), "\n")
	for y := len(lines) - 1; y >= 0; y-- {
		for _, text := range []string{"[ " + label + " ]", "[" + label + "]"} {
			if x := strings.Index(lines[y], text); x >= 0 {
				return ansi.StringWidth(lines[y][:x]), y, len(text)
			}
		}
	}
	t.Fatalf("missing %s button:\n%s", label, view)
	return 0, 0, 0
}

func TestPaneConfirmationMouseButtons(t *testing.T) {
	for _, size := range [][2]int{{16, 6}, {24, 8}, {64, 20}, {65, 21}} {
		for _, label := range []string{"Yes", "No"} {
			t.Run(fmt.Sprint(size)+label, func(t *testing.T) {
				m := &paneDialog{width: 64, height: 20, request: dialogRequest{Confirm: true,
					Lines: []string{strings.Repeat("A long message to review. ", 100)}}}
				m.View()
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				m.Update(tea.KeyMsg{Type: tea.KeyEnd})
				view := m.View()
				if len(strings.Split(view, "\n")) > size[1] {
					t.Fatal("buttons overflow the dialog height")
				}
				x, y, width := confirmationButtonCells(t, view, label)
				for _, msg := range []tea.MouseMsg{
					{X: x - 1, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress},
					{X: x + width, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress},
					{X: x, Y: y - 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress},
					{X: x, Y: y, Button: tea.MouseButtonRight, Action: tea.MouseActionPress},
					{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion},
					{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease},
					{X: x, Y: y, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress},
				} {
					if _, cmd := m.Update(msg); cmd != nil || m.approved {
						t.Fatalf("non-click answered the dialog: %+v", msg)
					}
				}
				for _, offset := range []int{0, width / 2, width - 1} {
					copy := *m
					_, cmd := copy.Update(tea.MouseMsg{X: x + offset, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
					if cmd == nil || copy.approved != (label == "Yes") {
						t.Fatalf("click on %s at offset %d did not answer correctly", label, offset)
					}
					if _, ok := cmd().(tea.QuitMsg); !ok {
						t.Fatal("click did not dismiss the popup")
					}
				}
			})
		}
	}
}

func TestSidebarConfirmationMouseButtons(t *testing.T) {
	for _, size := range [][2]int{{16, 10}, {24, 12}, {46, 24}, {65, 25}} {
		for _, label := range []string{"Yes", "No"} {
			t.Run(fmt.Sprint(size)+label, func(t *testing.T) {
				m := uxModel(t)
				called := 0
				m.confirm(strings.Repeat("A long confirmation. ", 100), func() tea.Cmd { called++; return nil })
				m.View()
				m.width, m.height, m.scroll = size[0], size[1], 1<<20
				x, y, width := confirmationButtonCells(t, m.View(), label)
				m.Update(tea.MouseMsg{X: x, Y: y - 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
				if m.mode != modeConfirm || called != 0 {
					t.Fatal("click outside the buttons answered the confirmation")
				}
				m.Update(tea.MouseMsg{X: x + width/2, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
				m.Update(tea.MouseMsg{X: x + width/2, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
				want := 0
				if label == "Yes" {
					want = 1
				}
				if m.mode != modeNormal || m.confirmFn != nil || called != want {
					t.Fatalf("%s click: mode=%v, callback ran %d times", label, m.mode, called)
				}
			})
		}
	}
}

func TestPaneConfirmationOwnsItsAnswer(t *testing.T) {
	m := uxModel(t)
	m.selfBin = "/unused/agentboss"
	m.dialogOrigin = tmuxctl.PopupOrigin{Pane: "%source"}
	called := 0
	m.confirm("Stop the captured session?", func() tea.Cmd { called++; return nil })
	if !m.dialogActive || len(m.pending) != 1 || strings.Contains(ansi.Strip(m.View()), "Stop the captured session?") {
		t.Fatal("pane confirmation also appeared in the sidebar")
	}
	m.pending = nil // Update dispatches the popup command before receiving its result.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if called != 0 {
		t.Fatal("a sidebar key approved the pane confirmation")
	}
	m.dialogOrigin = tmuxctl.PopupOrigin{} // keep the state-machine check independent of tmux
	m.finishPaneDialog(dialogResult{ID: m.dialogID + 1, Approved: true})
	if called != 0 || !m.dialogActive {
		t.Fatal("an unrelated popup response changed the confirmation")
	}
	m.finishPaneDialog(dialogResult{ID: m.dialogID})
	if called != 0 || m.mode != modeNormal || m.confirmFn != nil {
		t.Fatal("canceling retained or ran the action")
	}

	m.dialogOrigin = tmuxctl.PopupOrigin{Pane: "%source"}
	m.confirm("Stop the captured session?", func() tea.Cmd { called++; return nil })
	m.pending = nil
	m.dialogOrigin = tmuxctl.PopupOrigin{}
	result := dialogResult{ID: m.dialogID, Approved: true}
	m.finishPaneDialog(result)
	m.finishPaneDialog(result)
	if called != 1 {
		t.Fatalf("approval ran %d times, want exactly once", called)
	}
}

func TestPaneDialogFallbackStillRequiresApproval(t *testing.T) {
	m := uxModel(t)
	m.mode = modeConfirm
	m.dialogActive = true
	m.dialogID = 1
	m.confirmMsg = "Stop session?"
	called := false
	m.confirmFn = func() tea.Cmd { called = true; return nil }
	m.finishPaneDialog(dialogResult{ID: 1, Fallback: true})
	if called || m.dialogActive || m.mode != modeConfirm || !strings.Contains(ansi.Strip(m.View()), "Stop session?") {
		t.Fatal("popup fallback did not preserve an unanswered confirmation")
	}
	m.keyConfirm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if !called {
		t.Fatal("fallback confirmation could not be answered")
	}
}
