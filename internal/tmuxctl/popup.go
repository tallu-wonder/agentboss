package tmuxctl

import (
	"fmt"
	"os/exec"
)

// PopupOrigin records the pane and client where an action was requested.
// Session is the agent shown there at that moment, before a later tab switch.
type PopupOrigin struct {
	Pane    string `json:"pane,omitempty"`
	Client  string `json:"client,omitempty"`
	Session string `json:"session,omitempty"`
}

func CapturePopupOrigin(pane, client string) PopupOrigin {
	panes, err := Panes()
	if err != nil {
		return PopupOrigin{}
	}
	clients := ClientSessions()
	for _, p := range panes {
		if pane != "" && p.ID != pane || pane == "" && !p.Active {
			continue
		}
		if p.Role != "sidebar" && p.Role != "viewport" {
			return PopupOrigin{}
		}
		return PopupOrigin{Pane: p.ID, Client: client, Session: clients[p.TTY]}
	}
	return PopupOrigin{}
}

type PopupTarget struct {
	Origin PopupOrigin
	W, H   int
}

// ResolvePopup only targets a viewport and a client attached to this manager.
// Headless desks and panes too small for a dialog use the sidebar instead.
func ResolvePopup(origin PopupOrigin) (PopupTarget, error) {
	panes, err := Panes()
	if err != nil {
		return PopupTarget{}, err
	}
	for _, p := range panes {
		if p.ID != origin.Pane || p.Role != "viewport" || p.Dead {
			continue
		}
		if p.W < 26 || p.H < 10 {
			break
		}
		clients := ClientSessions()
		if origin.Client != "" && clients[origin.Client] != ManagerSession {
			break
		}
		if origin.Client == "" {
			for tty, session := range clients {
				if session == ManagerSession {
					if origin.Client != "" {
						return PopupTarget{}, fmt.Errorf("dialog client is ambiguous")
					}
					origin.Client = tty
				}
			}
		}
		if origin.Client != "" {
			return PopupTarget{Origin: origin, W: p.W - 4, H: p.H - 2}, nil
		}
	}
	return PopupTarget{}, fmt.Errorf("dialog pane or client is unavailable")
}

// Show centers the popup over its source pane. Tmux's popup coordinates account
// for the status line and the client's view of the window.
func (p PopupTarget) Show(command string, w, h int) error {
	x := "#{e|+|:#{popup_pane_left},#{e|/|:#{e|-|:#{pane_width},#{popup_width}},2}}"
	y := "#{e|+|:#{popup_pane_top},#{e|/|:#{e|-|:#{pane_height},#{popup_height}},2}}"
	return exec.Command("tmux", "display-popup", "-E", "-B",
		"-c", p.Origin.Client, "-t", p.Origin.Pane,
		"-x", x, "-y", y, "-w", fmt.Sprint(w), "-h", fmt.Sprint(h), command).Run()
}
