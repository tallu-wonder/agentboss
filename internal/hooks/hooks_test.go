package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallFreshFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	changed, err := Install(path, "/usr/local/bin/agentboss")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("fresh install should report a change")
	}
	root := readJSON(t, path)
	hooks := root["hooks"].(map[string]any)
	for _, ev := range Events {
		if _, ok := hooks[ev]; !ok {
			t.Fatalf("event %s missing", ev)
		}
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if _, err := Install(path, "/bin/agentboss"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	changed, err := Install(path, "/bin/agentboss")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second install must be a no-op")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("file changed on idempotent install")
	}
}

func TestInstallPreservesExistingSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := map[string]any{
		"model": "opus",
		"permissions": map[string]any{
			"allow": []any{"Bash(ls:*)"},
		},
		"hooks": map[string]any{
			"Stop": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{"type": "command", "command": "afplay /System/Library/Sounds/Glass.aiff"},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(existing)
	os.WriteFile(path, data, 0o644)

	if _, err := Install(path, "/bin/agentboss"); err != nil {
		t.Fatal(err)
	}
	root := readJSON(t, path)
	if root["model"] != "opus" {
		t.Fatal("unrelated top-level key lost")
	}
	perms := root["permissions"].(map[string]any)
	if len(perms["allow"].([]any)) != 1 {
		t.Fatal("permissions lost")
	}
	stops := root["hooks"].(map[string]any)["Stop"].([]any)
	if len(stops) != 2 {
		t.Fatalf("expected user hook + agentboss hook on Stop, got %d entries", len(stops))
	}
	// user's entry must still be first and intact
	first := stops[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if first["command"] != "afplay /System/Library/Sounds/Glass.aiff" {
		t.Fatal("user hook clobbered")
	}
}

func TestInstallHealsStaleBinaryPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if _, err := Install(path, "/old/place/agentboss"); err != nil {
		t.Fatal(err)
	}
	changed, err := Install(path, "/new/place/agentboss")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("moving the binary must rewrite the hook commands")
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "/old/place/") {
		t.Fatal("stale path left behind")
	}
	if !strings.Contains(string(data), "'/new/place/agentboss' hook") {
		t.Fatalf("new quoted path missing: %s", data)
	}
	// and it must not have duplicated entries
	root := readJSON(t, path)
	stops := root["hooks"].(map[string]any)["Stop"].([]any)
	if len(stops) != 1 {
		t.Fatalf("expected 1 Stop entry, got %d", len(stops))
	}
}

func TestInstallRefusesWrongTypes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"hooks": ["not-a-map"]}`), 0o644)
	if _, err := Install(path, "/bin/agentboss"); err == nil {
		t.Fatal("must refuse an unexpected hooks structure")
	}
	os.WriteFile(path, []byte(`{"hooks": {"Stop": {"bad": true}}}`), 0o644)
	if _, err := Install(path, "/bin/agentboss"); err == nil {
		t.Fatal("must refuse an unexpected event entry type")
	}
}

func TestInstallRefusesCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte("{not json"), 0o644)
	if _, err := Install(path, "/bin/agentboss"); err == nil {
		t.Fatal("must refuse to rewrite a corrupt settings file")
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestIsOursIsStrict(t *testing.T) {
	for _, ours := range []string{
		"'/Users/me/.local/bin/agentboss' hook",
		"/usr/local/bin/agentboss hook",
		"agentboss hook",
	} {
		if !isOurs(ours) {
			t.Errorf("%q should be recognized as ours", ours)
		}
	}
	for _, foreign := range []string{
		"notify.sh && agentboss hook",   // a wrapper we must not clobber
		"agentboss hook | tee /tmp/log", // user piped our output somewhere
		"agentboss hook --verbose",      // not a command we write
		"my-agentboss-wrapper hook-all", // similar name, different tool
		"'/bin/other' hook",             // someone else's hook
		"echo agentboss hook",           // mentions us, isn't us
	} {
		if isOurs(foreign) {
			t.Errorf("%q must NOT be treated as ours (it would be overwritten)", foreign)
		}
	}
}

func TestInstallBacksUpTheOriginalOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := `{"model":"opus","hooks":{}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(path, "/bin/agentboss"); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".agentboss-backup")
	if err != nil {
		t.Fatal("no backup written")
	}
	if string(backup) != original {
		t.Fatalf("backup = %s, want the pre-edit file", backup)
	}
	// A later install must not overwrite the pristine copy.
	if _, err := Install(path, "/bin/agentboss-moved"); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path + ".agentboss-backup")
	if string(again) != original {
		t.Fatalf("backup was overwritten: %s", again)
	}
}
