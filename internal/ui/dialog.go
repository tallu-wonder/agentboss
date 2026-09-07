package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/tallu-wonder/agentboss/internal/tmuxctl"
)

type dialogRequest struct {
	Lines   []string `json:"lines"`
	Confirm bool     `json:"confirm"`
}

type dialogResult struct {
	ID       uint64
	Approved bool
	Fallback bool
}

type dialogAnswer struct {
	Approved bool `json:"approved"`
}

// The manager keeps ownership of the callback and its captured session IDs.
// The popup only displays the message and reports an explicit answer.
func (m *Model) startPaneDialog(lines []string, confirm bool) bool {
	if m.dialogOrigin.Pane == "" || m.dialogOrigin.Pane == m.sidebarPane || m.selfBin == "" {
		return false
	}
	m.dialogID++
	m.dialogActive = true
	if m.sidebarPane != "" {
		tmuxctl.SetDialogActive(true)
	}
	if !confirm {
		m.confirmFn = nil
	}
	id, origin, bin := m.dialogID, m.dialogOrigin, m.selfBin
	req := dialogRequest{Lines: append([]string(nil), lines...), Confirm: confirm}
	m.pending = append(m.pending, func() tea.Msg {
		return runPaneDialog(id, origin, bin, req)
	})
	return true
}

func runPaneDialog(id uint64, origin tmuxctl.PopupOrigin, bin string, req dialogRequest) dialogResult {
	result := dialogResult{ID: id, Fallback: true}
	target, err := tmuxctl.ResolvePopup(origin)
	if err != nil {
		return result
	}
	dir, err := os.MkdirTemp("", "agentboss-dialog-")
	if err != nil {
		return result
	}
	defer os.RemoveAll(dir)
	data, err := json.Marshal(req)
	if err != nil {
		return result
	}
	requestPath := filepath.Join(dir, "request.json")
	if os.WriteFile(requestPath, data, 0600) != nil {
		return result
	}
	w := min(64, target.W)
	foot, _ := dialogFooter(req.Confirm, w, target.H)
	h := len(strings.Split(ansi.Wrap(strings.Join(req.Lines, "\n"), w-6, ""), "\n")) +
		len(foot) + 3
	h = min(target.H, min(32, max(8, h)))
	if tmuxctl.SelectPane(target.Origin.Pane) != nil {
		return result
	}
	// A successful tmux command is not an approval. Escape, a closed client,
	// or a failed child process all leave the answer unapproved.
	err = target.Show(shellQuote(bin)+" __dialog "+shellQuote(requestPath), w, h)
	_, readyErr := os.Stat(filepath.Join(dir, "ready"))
	result.Fallback = err != nil && readyErr != nil
	if data, readErr := os.ReadFile(filepath.Join(dir, "answer.json")); readErr == nil {
		var answer dialogAnswer
		if json.Unmarshal(data, &answer) == nil && err == nil {
			result.Approved = answer.Approved
		}
	}
	return result
}

func (m *Model) finishPaneDialog(result dialogResult) (tea.Model, tea.Cmd) {
	if !m.dialogActive || result.ID != m.dialogID {
		return m, nil
	}
	m.dialogActive = false
	if m.sidebarPane != "" {
		tmuxctl.SetDialogActive(false)
	}
	if result.Fallback {
		m.focusSidebar()
		return m, nil
	}
	fn := m.confirmFn
	m.confirmFn = nil
	m.mode = modeNormal
	m.scroll = 0
	if m.dialogOrigin.Pane != "" {
		_ = tmuxctl.SelectPane(m.dialogOrigin.Pane)
	}
	if result.Approved && fn != nil {
		return m, m.withPending(fn())
	}
	return m, nil
}

func (m *Model) withPending(cmd tea.Cmd) tea.Cmd {
	if len(m.pending) == 0 {
		return cmd
	}
	commands := append(m.pending, cmd)
	m.pending = nil
	return tea.Batch(commands...)
}

func dialogHint(confirm bool) string {
	if confirm {
		return "y confirm · n / Esc cancel"
	}
	return "↑↓ / PgUp PgDn scroll · Esc close"
}

type dialogButton struct {
	x, y, width int
	key         string
}

func dialogFooter(confirm bool, width, height int) ([]string, []dialogButton) {
	w := max(8, width-6)
	hint := scrollBoxHint(dialogHint(confirm), w)
	if !confirm {
		return hint, nil
	}
	yes, no, gap := "[ Yes ]", "[ No ]", "  "
	if w < len(yes)+len(gap)+len(no) {
		yes, no, gap = "[Yes]", "[No]", " "
	}
	left := max(0, (w-len(yes)-len(gap)-len(no))/2)
	buttons := []dialogButton{
		{x: left, width: len(yes), key: "y"},
		{x: left + len(yes) + len(gap), width: len(no), key: "n"},
	}
	foot := []string{strings.Repeat(" ", left) +
		stActive.Background(cSelBg).Render(yes) + gap +
		stText.Bold(true).Background(cSelBg).Render(no)}
	// Keep both buttons and at least one message row visible in short panes.
	if height >= len(hint)+5 {
		foot = append(foot, hint...)
	}
	return foot, buttons
}

func renderDialogBox(lines []string, confirm bool, width, height int, scroll *int) (string, []dialogButton) {
	foot, buttons := dialogFooter(confirm, width, height)
	box, footY := renderScrollBoxFooter(lines, foot, width, height, scroll)
	for i := range buttons {
		buttons[i].x += stOverlay.GetBorderLeftSize() + stOverlay.GetPaddingLeft()
		buttons[i].y += footY
	}
	return box, buttons
}

func dialogClick(msg tea.MouseMsg, buttons []dialogButton, left, top int) string {
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress {
		return ""
	}
	for _, button := range buttons {
		if msg.Y == top+button.y && msg.X >= left+button.x && msg.X < left+button.x+button.width {
			return button.key
		}
	}
	return ""
}

func (m *Model) confirmClick(msg tea.MouseMsg) string {
	if m.width < 16 || m.height < 10 {
		return ""
	}
	box, buttons := renderDialogBox(m.confirmLines(), true, m.width, m.listInnerHeight(), &m.scroll)
	return dialogClick(msg, buttons, max(0, (m.width-lipgloss.Width(box))/2),
		m.listTopY()+max(0, (m.listInnerHeight()-lipgloss.Height(box))/2))
}

type paneDialog struct {
	request       dialogRequest
	width, height int
	scroll        int
	approved      bool
}

func (m *paneDialog) Init() tea.Cmd { return nil }

func (m *paneDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "y", "Y":
			if m.request.Confirm {
				m.approved = true
				return m, tea.Quit
			}
		case "n", "N", "esc", "q", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if !m.request.Confirm {
				return m, tea.Quit
			}
		case "up":
			m.scroll = max(0, m.scroll-1)
		case "down":
			m.scroll++
		case "pgup":
			m.scroll = max(0, m.scroll-max(1, m.height-4))
		case "pgdown":
			m.scroll += max(1, m.height-4)
		case "home":
			m.scroll = 0
		case "end":
			m.scroll = 1 << 20
		}
	case tea.MouseMsg:
		if m.request.Confirm && m.width >= 16 && m.height >= 6 {
			box, buttons := renderDialogBox(m.request.Lines, true, m.width, m.height, &m.scroll)
			if key := dialogClick(msg, buttons, max(0, (m.width-lipgloss.Width(box))/2),
				max(0, (m.height-lipgloss.Height(box))/2)); key != "" {
				m.approved = key == "y"
				return m, tea.Quit
			}
		}
		if msg.Button == tea.MouseButtonWheelUp {
			m.scroll = max(0, m.scroll-3)
		} else if msg.Button == tea.MouseButtonWheelDown {
			m.scroll += 3
		}
	}
	return m, nil
}

func (m *paneDialog) View() string {
	if m.width < 16 || m.height < 6 {
		return "Resize to view dialog · Esc cancels"
	}
	box, _ := renderDialogBox(m.request.Lines, m.request.Confirm, m.width, m.height, &m.scroll)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// RunPaneDialog is the internal popup process, without manager startup or any
// agent/session mutations. Only its parent manager can apply the answer.
func RunPaneDialog(requestPath string) error {
	data, err := os.ReadFile(requestPath)
	if err != nil {
		return err
	}
	m := &paneDialog{}
	if err := json.Unmarshal(data, &m.request); err != nil {
		return err
	}
	dir := filepath.Dir(requestPath)
	if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600); err != nil {
		return err
	}
	if _, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		return fmt.Errorf("dialog: %w", err)
	}
	answer, _ := json.Marshal(dialogAnswer{Approved: m.approved})
	return os.WriteFile(filepath.Join(dir, "answer.json"), answer, 0600)
}
