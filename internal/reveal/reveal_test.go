package reveal

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCommandUsesThePlatformFileManager(t *testing.T) {
	t.Setenv(EnvCmd, "")
	dir := t.TempDir()
	argv, err := Command(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"darwin": "open", "windows": "explorer"}[runtime.GOOS]
	if want == "" {
		want = "xdg-open"
	}
	if argv[0] != want {
		t.Errorf("opener = %q, want %q", argv[0], want)
	}
	if argv[len(argv)-1] != dir {
		t.Errorf("folder must be the last argument, got %v", argv)
	}
}

// The override carries arguments so an editor invocation works ("code -n").
func TestCommandHonorsTheUsersOpener(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "myeditor")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv(EnvCmd, "myeditor -n --wait")

	argv, err := Command(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv, " "); got != "myeditor -n --wait "+dir {
		t.Errorf("argv = %q", got)
	}
}

// A session can outlive its folder; that must read as an error, not a no-op.
func TestCommandRejectsWhatItCannotOpen(t *testing.T) {
	t.Setenv(EnvCmd, "")
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"missing":   filepath.Join(t.TempDir(), "nope"),
		"not a dir": file,
		"empty":     "",
	} {
		if _, err := Command(dir); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// A configured program that isn't installed must say so by name.
	t.Setenv(EnvCmd, "definitely-not-installed-xyz")
	_, err := Command(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "definitely-not-installed-xyz") {
		t.Errorf("err = %v, want it to name the missing program", err)
	}
}

// Dir must not block the UI or wait for the program to exit.
func TestDirLaunchesWithoutWaiting(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "slowopener")
	script := "#!/bin/sh\nsleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv(EnvCmd, "slowopener")

	done := make(chan error, 1)
	go func() { done <- Dir(dir) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Dir blocked on the opener instead of returning immediately")
	}
}
