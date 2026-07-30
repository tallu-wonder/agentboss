package codexnotify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realConfig mirrors the shape of a working ~/.codex/config.toml, including
// an existing notify integration that must survive chaining.
const realConfig = `approval_policy = 'on-request'
model = 'gpt-5.6-sol'
notify = ["/Applications/TurnBell.app/Contents/MacOS/TurnBell", "turn-ended"]

[mcp_servers]

[mcp_servers.node_repl]
command = "/usr/local/bin/node_repl"
`

func setup(t *testing.T, cfg string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("AGENTDECK_HOME", filepath.Join(home, "deck"))
	os.MkdirAll(filepath.Join(home, "deck"), 0o755)
	codex := filepath.Join(home, "codex")
	os.MkdirAll(codex, 0o755)
	t.Setenv("AGENTDECK_CODEX_HOME", codex)
	if cfg != "" {
		if err := os.WriteFile(filepath.Join(codex, "config.toml"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(codex, "config.toml")
}

func TestInstallChainsAndPreservesExistingNotify(t *testing.T) {
	cfgPath := setup(t, realConfig)
	what, err := Install("/bin/agentdeck", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(what, "forwarding") {
		t.Fatalf("expected a forwarding chain, got %q", what)
	}
	out, _ := os.ReadFile(cfgPath)
	got := string(out)

	if !strings.Contains(got, `notify = ["/bin/agentdeck", "codex-notify"]`) {
		t.Fatalf("notify not rewritten:\n%s", got)
	}
	// Everything unrelated survives.
	for _, keep := range []string{"approval_policy = 'on-request'", "model = 'gpt-5.6-sol'",
		"[mcp_servers.node_repl]", "/usr/local/bin/node_repl"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("lost %q from config:\n%s", keep, got)
		}
	}
	// The displaced program is recorded for forwarding.
	chain := LoadChain()
	if len(chain.Program) != 2 || !strings.Contains(chain.Program[0], "TurnBell") ||
		chain.Program[1] != "turn-ended" {
		t.Fatalf("chain not recorded: %+v", chain)
	}
	// A backup of the original exists.
	if _, err := os.Stat(cfgPath + ".agentdeck-backup"); err != nil {
		t.Fatal("no backup written")
	}
	if !Installed("/bin/agentdeck") {
		t.Fatal("Installed() should report true")
	}
	// Idempotent.
	if what, err := Install("/bin/agentdeck", true); err != nil || what != "already installed" {
		t.Fatalf("second install = %q, %v", what, err)
	}
}

func TestUninstallRestoresOriginal(t *testing.T) {
	cfgPath := setup(t, realConfig)
	if _, err := Install("/bin/agentdeck", true); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(out), "TurnBell") || strings.Contains(string(out), "agentdeck") {
		t.Fatalf("original notify not restored:\n%s", out)
	}
}

func TestInstallWithNoExistingNotify(t *testing.T) {
	cfgPath := setup(t, "model = 'gpt-5.6-sol'\n\n[mcp_servers]\n")
	if _, err := Install("/bin/agentdeck", false); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(cfgPath)
	got := string(out)
	if !strings.Contains(got, `notify = ["/bin/agentdeck", "codex-notify"]`) {
		t.Fatalf("notify not added:\n%s", got)
	}
	// The assignment must precede any [table] header, or TOML would scope it
	// to that table.
	if strings.Index(got, "notify =") > strings.Index(got, "[mcp_servers]") {
		t.Fatalf("notify landed inside a table:\n%s", got)
	}
	if len(LoadChain().Program) != 0 {
		t.Fatal("nothing was displaced; chain should be empty")
	}
}

func TestParseEventFromRealPayload(t *testing.T) {
	// Exactly what Codex passed our probe program.
	args := []string{"turn-ended", `{"type":"agent-turn-complete","thread-id":"019f9db9-dac6-7b13-94b9-8095c66ad92a","turn-id":"019f9db9-db7a","cwd":"/tmp/x","client":"codex_exec","input-messages":["hi"],"last-assistant-message":"ok"}`}
	ev, ok := ParseEvent(args)
	if !ok {
		t.Fatal("payload not parsed")
	}
	if ev.Type != "agent-turn-complete" {
		t.Fatalf("type = %q", ev.Type)
	}
	if ev.ThreadID != "019f9db9-dac6-7b13-94b9-8095c66ad92a" {
		t.Fatalf("thread id = %q", ev.ThreadID)
	}
	if _, ok := ParseEvent([]string{"turn-ended"}); ok {
		t.Fatal("no JSON argument should mean no event")
	}
}

func TestForwardIsSafeWithoutChain(t *testing.T) {
	setup(t, realConfig)
	Forward([]string{"turn-ended", "{}"}) // must not panic with no chain file
}

// chainedByAnotherTool is the shape a competing tool leaves behind: it takes the
// notify slot back and embeds agentdeck's argv as an escaped JSON string. The
// ']' inside that string is what a careless pattern stops at, and failing to see
// this line as a notify assignment means writing a second one — a duplicate TOML
// key, which stops Codex from starting.
const chainedByAnotherTool = `model = 'gpt-5.6-sol'
notify = ["/Applications/TurnBell.app/Contents/MacOS/TurnBell", "turn-ended", "--previous-notify", "[\"\\/Users\\/me\\/.local\\/bin\\/agentdeck\",\"codex-notify\"]"]

[mcp_servers]
`

func TestInstallRefusesToFightAnotherToolForTheSlot(t *testing.T) {
	cfgPath := setup(t, realConfig)
	if _, err := Install("/bin/agentdeck", false); err == nil {
		t.Fatal("must refuse to displace another program without --force")
	}
	body, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(body), "agentdeck") {
		t.Fatalf("config must be untouched on refusal:\n%s", body)
	}
	// With --force it chains, as before.
	if _, err := Install("/bin/agentdeck", true); err != nil {
		t.Fatal(err)
	}
	if n := len(notifyLines(cfgPath)); n != 1 {
		t.Fatalf("expected exactly one notify line, got %d", n)
	}
}

func TestRecognizesBeingChainedByAnotherTool(t *testing.T) {
	cfgPath := setup(t, chainedByAnotherTool)
	// The other tool forwards to agentdeck, so its events already arrive; adding
	// a second notify line would be a duplicate key and stop Codex starting.
	if !Installed("/Users/me/.local/bin/agentdeck") {
		t.Fatal("a notify line that forwards to agentdeck must count as installed")
	}
	what, err := Install("/Users/me/.local/bin/agentdeck", true)
	if err != nil || what != "already installed" {
		t.Fatalf("Install = %q, %v; want a no-op", what, err)
	}
	if n := len(notifyLines(cfgPath)); n != 1 {
		t.Fatalf("still exactly one notify line expected, got %d", n)
	}
}

func TestInstallRefusesWhenTheFileAlreadyHasDuplicates(t *testing.T) {
	setup(t, "notify = [\"/a\"]\nnotify = [\"/b\"]\n")
	if _, err := Install("/bin/agentdeck", true); err == nil {
		t.Fatal("must refuse to edit a config Codex cannot parse")
	}
}
