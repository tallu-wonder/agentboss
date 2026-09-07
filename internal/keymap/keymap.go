// Package keymap owns the desk's action names and global shortcut definitions.
package keymap

import "runtime"

type Action struct {
	Key     string // tmux suffix after M-
	Label   string
	Sidebar bool // needs the keyboard in the sidebar
}

var Actions = []Action{
	{"p", "commands", true}, {"n", "new session", true},
	{"W", "new session in a git worktree", true}, {"i", "import past conversation", true},
	{"/", "search sessions", true}, {"A", "needs attention view", true},
	{"e", "filter by agent or project", true},
	{"N", "new group", true}, {"r", "rename", true}, {"m", "move selected sessions to group", true},
	{"b", "select / deselect session", true}, {"B", "select all visible sessions", true},
	{"J", "move down", false}, {"K", "move up", false},
	{"s", "sort sessions", true}, {"v", "session info", true},
	{"t", "show / hide row metrics", false},
	{"f", "open project folder", false}, {"F", "open scratch folder", false},
	{"M", "mute / unmute notifications", false}, {"c", "choose group color", true},
	{"h", "go to group header / fold group", true}, {"Space", "fold / unfold group", true},
	{"z", "stop session", true}, {"x", "archive / remove from desk", true},
	{"u", "undo stop or archive", true}, {"q", "quit manager (agents keep running)", true},
	{"<", "narrower sidebar", false}, {">", "wider sidebar", false}, {"?", "keys and status legend", true},
}

func Keys() []string {
	keys := make([]string, 0, len(Actions))
	for _, a := range Actions {
		keys = append(keys, a.Key)
	}
	return keys
}

func Display(k string) string {
	if k == "Space" {
		k = "space"
	}
	if runtime.GOOS == "darwin" {
		return "⌥" + k
	}
	return "alt+" + k
}

func Hint(key, label string) string { return Display(key) + " " + label }
