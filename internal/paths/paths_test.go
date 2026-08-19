package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// The upgrade path from the project's pre-rename name: the old desk moves, a
// symlink keeps old binaries and their installed hooks writing into it.
func TestMigrateLegacyHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTBOSS_HOME", "")
	oldP := filepath.Join(home, ".agentdeck")
	newP := filepath.Join(home, ".agentboss")

	// Nothing to migrate: a no-op, and no stray directories.
	MigrateLegacyHome()
	if _, err := os.Lstat(oldP); !os.IsNotExist(err) {
		t.Fatal("no legacy desk existed, nothing should have been created")
	}

	// A legacy desk moves and leaves a symlink behind.
	if err := os.MkdirAll(filepath.Join(oldP, "status"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldP, "state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	MigrateLegacyHome()
	if _, err := os.Stat(filepath.Join(newP, "state.json")); err != nil {
		t.Fatalf("desk did not move: %v", err)
	}
	link, err := os.Readlink(oldP)
	if err != nil || link != newP {
		t.Fatalf("old path should be a symlink to the new desk, got %q, %v", link, err)
	}
	// Writes through the old path (an old binary's hooks) land in the new desk.
	if err := os.WriteFile(filepath.Join(oldP, "status", "s_1.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(newP, "status", "s_1.json")); err != nil {
		t.Fatal("a write through the legacy symlink should land in the new desk")
	}

	// Running again is a no-op (the symlink is not migrated onto itself).
	MigrateLegacyHome()
	if link, _ := os.Readlink(oldP); link != newP {
		t.Fatal("second run must leave the symlink alone")
	}

	// Both real directories present: never guess, never touch.
	os.Remove(oldP)
	if err := os.MkdirAll(oldP, 0o700); err != nil {
		t.Fatal(err)
	}
	MigrateLegacyHome()
	if fi, err := os.Lstat(oldP); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("with two real desks, migration must not pick one")
	}

	// An explicit AGENTBOSS_HOME opts out entirely.
	t.Setenv("AGENTBOSS_HOME", filepath.Join(home, "elsewhere"))
	MigrateLegacyHome()
}
