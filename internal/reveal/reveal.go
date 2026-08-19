// Package reveal opens a session's working folder in the desktop — Finder on
// macOS, the XDG handler elsewhere — or in whatever program the user prefers.
//
// It is deliberately fire-and-forget: the folder opens in another application,
// so agentboss must never block its event loop waiting for it, and must never
// let that program write to the terminal it shares with the TUI.
package reveal

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// EnvCmd names the program used to open a folder. It may carry arguments
// ("code -n", "cursor", "nvim"), and the folder is appended as the last one.
const EnvCmd = "AGENTBOSS_OPEN_CMD"

// Command builds the argv that opens dir, without running it. Callers get an
// error for a folder that no longer exists — a session can outlive the
// directory it was started in, and silently doing nothing looks like a bug.
func Command(dir string) ([]string, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("this session has no folder")
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("%s is gone", dir)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	argv := opener()
	if len(argv) == 0 {
		return nil, fmt.Errorf("set %s to the program that should open folders", EnvCmd)
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return nil, fmt.Errorf("%s not found", argv[0])
	}
	return append(argv, dir), nil
}

// opener resolves the program to use: the user's choice, else the platform's
// own file manager.
func opener() []string {
	if custom := strings.Fields(os.Getenv(EnvCmd)); len(custom) > 0 {
		return custom
	}
	return platformOpener()
}

// Dir opens dir and returns as soon as the program is launched. Its output is
// discarded and its stdin detached, so a GUI helper or a misconfigured
// AGENTBOSS_OPEN_CMD cannot scribble over the sidebar; the process is reaped in
// the background so we leave no zombies behind.
func Dir(dir string) error {
	argv, err := Command(dir)
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", argv[0], err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// platformOpener is the OS handler that respects the user's default browser.
func platformOpener() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"open"}
	case "windows":
		return []string{"explorer"}
	default:
		return []string{"xdg-open"}
	}
}

// shellQuote wraps
