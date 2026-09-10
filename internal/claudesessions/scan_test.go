package claudesessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeTranscript(t *testing.T, root, proj, sid, content string) string {
	t.Helper()
	dir := filepath.Join(root, proj)
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTitlePrefersNewestSummary(t *testing.T) {
	root := t.TempDir()
	p := writeTranscript(t, root, "-proj", "sid-1", `
{"type":"summary","summary":"Old title"}
{"type":"user","cwd":"/tmp","message":{"role":"user","content":"hi there"}}
{"type":"assistant","message":{"role":"assistant","content":"hello"}}
{"type":"summary","summary":"Fix stale DNS records"}
`)
	if got := Title(p); got != "Fix stale DNS records" {
		t.Fatalf("Title = %q", got)
	}
}

func TestTitleFallsBackToFirstUserMessage(t *testing.T) {
	root := t.TempDir()
	p := writeTranscript(t, root, "-proj", "sid-2",
		`{"type":"user","cwd":"/tmp","message":{"role":"user","content":"sweep the open tickets and group them by area"}}`+"\n")
	if got := Title(p); got != "sweep the open tickets and group them by area" {
		t.Fatalf("Title = %q", got)
	}
}

func TestTranscriptPathFindsAcrossProjects(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBOSS_CLAUDE_PROJECTS", root)
	writeTranscript(t, root, "-proj-a", "aaa", "{}\n")
	want := writeTranscript(t, root, "-proj-b", "bbb", "{}\n")
	if got := TranscriptPath("bbb"); got != want {
		t.Fatalf("TranscriptPath = %q, want %q", got, want)
	}
	if got := TranscriptPath("zzz"); got != "" {
		t.Fatalf("missing session should give empty path, got %q", got)
	}
}

func TestTitlePrefersExplicitSessionName(t *testing.T) {
	root := t.TempDir()
	p := writeTranscript(t, root, "-proj", "sid-3", `
{"type":"summary","summary":"A summary title"}
{"type":"user","cwd":"/tmp","message":{"role":"user","content":"hello"}}
{"type":"custom-title","customTitle":"cost-report","sessionId":"sid-3"}
{"type":"agent-name","agentName":"cost-report-bot","sessionId":"sid-3"}
`)
	if got := Title(p); got != "cost-report-bot" {
		t.Fatalf("Title = %q, want the newest explicit name", got)
	}
}

func TestAppendRenameRoundTrip(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBOSS_CLAUDE_PROJECTS", root)
	p := writeTranscript(t, root, "-proj", "sid-4",
		`{"type":"user","cwd":"/tmp","message":{"role":"user","content":"first message"}}`+"\n")
	if err := AppendRename("sid-4", "shiny new name"); err != nil {
		t.Fatal(err)
	}
	if got := Title(p); got != "shiny new name" {
		t.Fatalf("Title after AppendRename = %q", got)
	}
	// scanner picks it up for imports too
	got := Scan(map[string]bool{}, 0)
	if len(got) != 1 || got[0].Title != "shiny new name" {
		t.Fatalf("Scan title = %+v", got)
	}
}

func TestScanExtractsTitlesAndDirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBOSS_CLAUDE_PROJECTS", root)
	writeTranscript(t, root, "-p", "s1", `{"type":"summary","summary":"Nice title"}
{"type":"user","cwd":"/tmp","message":{"role":"user","content":"x"}}`+"\n")
	writeTranscript(t, root, "-p", "s2", `{"type":"user","cwd":"/tmp","message":{"role":"user","content":[{"type":"text","text":"block content"}]}}`+"\n")
	got := Scan(map[string]bool{}, 0)
	if len(got) != 2 {
		t.Fatalf("expected 2 conversations, got %d", len(got))
	}
	byID := map[string]Conversation{}
	for _, c := range got {
		byID[c.SessionID] = c
	}
	if byID["s1"].Title != "Nice title" || byID["s1"].Dir != "/tmp" {
		t.Fatalf("s1 wrong: %+v", byID["s1"])
	}
	if byID["s2"].Title != "block content" {
		t.Fatalf("s2 wrong: %+v", byID["s2"])
	}
	// exclusion
	got = Scan(map[string]bool{"s1": true}, 0)
	if len(got) != 1 || got[0].SessionID != "s2" {
		t.Fatalf("exclude failed: %+v", got)
	}
}

func TestTailScanIgnoresSyntheticModel(t *testing.T) {
	root := t.TempDir()
	p := writeTranscript(t, root, "-p", "sid-syn", `
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8-20250514","usage":{"input_tokens":100,"output_tokens":10},"content":[]}}
{"type":"assistant","message":{"role":"assistant","model":"<synthetic>","usage":{"input_tokens":354000,"output_tokens":1},"content":[]}}
`)
	ti := TailScan(p)
	if ti.Model != "claude-opus-4-8-20250514" {
		t.Fatalf("synthetic model clobbered the real one: %q", ti.Model)
	}
	if ti.ContextTokens != 354001 {
		t.Fatalf("tokens should still track the last turn: %d", ti.ContextTokens)
	}
}

func TestCostDeltaIncremental(t *testing.T) {
	root := t.TempDir()
	p := writeTranscript(t, root, "-p", "sid-cost",
		`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":1000000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1000000},"content":[]}}`+"\n")
	c1, off, rescanned := CostDelta(p, 0)
	if rescanned {
		t.Fatal("first scan should not report rescanned")
	}
	// opus: $5/MTok in + $25/MTok out = $30
	if c1 < 29.99 || c1 > 30.01 {
		t.Fatalf("opus cost = %f, want 30", c1)
	}
	// append a fable turn with cache reads: 10*0.1=({1M cache read}) $1 + out 0
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant","message":{"role":"assistant","model":"claude-fable-5","usage":{"input_tokens":0,"cache_read_input_tokens":1000000,"output_tokens":0},"content":[]}}` + "\n")
	f.Close()
	c2, _, rescanned := CostDelta(p, off)
	if rescanned {
		t.Fatal("append should not rescan")
	}
	if c2 < 0.99 || c2 > 1.01 {
		t.Fatalf("fable cache-read cost = %f, want 1", c2)
	}
}

// ScratchDir must find a session's own working directory without reproducing
// Claude's cwd-mangling, and must prefer the scratchpad the agent writes into.
func TestScratchDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBOSS_CLAUDE_SCRATCH", root)

	// The real shape: /<root>/<mangled-cwd>/<session-id>/{scratchpad,tasks}
	withScratch := "11111111-1111-1111-1111-111111111111"
	mkdirAll(t, filepath.Join(root, "-Users-me-GitHub", withScratch, "scratchpad"))
	mkdirAll(t, filepath.Join(root, "-Users-me-GitHub", withScratch, "tasks"))
	// A session directory that exists but has no scratchpad yet.
	bare := "22222222-2222-2222-2222-222222222222"
	mkdirAll(t, filepath.Join(root, "-tmp-other", bare))

	if got, want := ScratchDir(withScratch), filepath.Join(root, "-Users-me-GitHub", withScratch, "scratchpad"); got != want {
		t.Errorf("ScratchDir = %q, want the scratchpad %q", got, want)
	}
	if got, want := ScratchDir(bare), filepath.Join(root, "-tmp-other", bare); got != want {
		t.Errorf("with no scratchpad, ScratchDir = %q, want the session dir %q", got, want)
	}
	// A session that never wrote anything, and the empty case, resolve to "".
	if got := ScratchDir("33333333-3333-3333-3333-333333333333"); got != "" {
		t.Errorf("unknown session = %q, want \"\"", got)
	}
	if got := ScratchDir(""); got != "" {
		t.Errorf("empty session id = %q, want \"\"", got)
	}
}

func mkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// A compaction shrinks the context but writes no usage record, so the size has
// to come from the compact record itself — otherwise the sidebar keeps showing
// the pre-compact number until the next turn.
func TestTailScanTakesContextFromACompaction(t *testing.T) {
	root := t.TempDir()
	turn := `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":500,"cache_creation_input_tokens":1000,"cache_read_input_tokens":755000,"output_tokens":874},"content":[]}}`
	boundary := `{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"manual","preTokens":757374,"postTokens":15771}}`

	// Compacted, with no turn since: the surviving context is what to show.
	p := writeTranscript(t, root, "-p", "sid-c1", turn+"\n"+boundary+"\n")
	if got := TailScan(p).ContextTokens; got != 15771 {
		t.Errorf("after a compaction: context = %d, want 15771 (postTokens)", got)
	}

	// A turn after the compaction is newer, so it wins.
	after := `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":16000,"output_tokens":100},"content":[]}}`
	p = writeTranscript(t, root, "-p", "sid-c2", turn+"\n"+boundary+"\n"+after+"\n")
	if got := TailScan(p).ContextTokens; got != 16120 {
		t.Errorf("after a post-compaction turn: context = %d, want 16120", got)
	}
}

// Rates drift, so the table must be overridable — and a broken override must
// never take the cost column down with it.
func TestPricesOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pricing.json")
	if err := os.WriteFile(path, []byte(`{"opus": [7, 35], "newmodel": [1, 2]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBOSS_PRICING", path)
	priceOnce = sync.Once{}
	priceTable = nil

	if in, out := price("opus"); in != 7 || out != 35 {
		t.Errorf("overridden opus = %v/%v, want 7/35", in, out)
	}
	if in, out := price("newmodel"); in != 1 || out != 2 {
		t.Errorf("added family = %v/%v, want 1/2", in, out)
	}
	if in, out := price("sonnet"); in != 3 || out != 15 {
		t.Errorf("untouched family = %v/%v, want the built-in 3/15", in, out)
	}
	if in, out := price("something-new"); in != 5 || out != 25 {
		t.Errorf("unknown family = %v/%v, want the top tier 5/25", in, out)
	}

	// Garbage in the file falls back to the built-ins rather than zero cost.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	priceOnce = sync.Once{}
	priceTable = nil
	if in, out := price("opus"); in != 5 || out != 25 {
		t.Errorf("broken price file: opus = %v/%v, want the built-in 5/25", in, out)
	}
}

// Claude's badge registry is written per process and can go stale while that
// process is still alive: a rename lands in the transcript and in the
// session's custom-title sidecar, and the registry file is not always
// rewritten. The desk treats a registry name as authoritative, so trusting a
// stale one pinned a session to Claude's cwd-derived auto-label and no later
// rename could ever win. The newer of the two must be the name.
func TestLiveNamePrefersTheNewerTitle(t *testing.T) {
	root := t.TempDir()
	reg := t.TempDir()
	t.Setenv("AGENTBOSS_CLAUDE_PROJECTS", root)
	t.Setenv("AGENTBOSS_CLAUDE_SESSIONS", reg)

	proj := filepath.Join(root, "-Users-dev-project")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	// registry entry, published an hour ago by a process that is still alive
	write := func(sid, name string, at time.Time) {
		rec := fmt.Sprintf(`{"sessionId":%q,"name":%q,"pid":%d,"updatedAt":%d}`,
			sid, name, os.Getpid(), at.UnixMilli())
		if err := os.WriteFile(filepath.Join(reg, sid[:8]+".json"), []byte(rec), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// sidecar title, written after the registry entry
	sidecar := func(sid, title string, at time.Time) {
		dir := filepath.Join(proj, sid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "custom-title.json")
		if err := os.WriteFile(p, []byte(fmt.Sprintf(`{"customTitle":%q}`, title)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	transcript := func(sid string) {
		if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()

	// renamed after the registry went stale: the rename wins
	renamed := "11111111-1111-4111-8111-111111111111"
	transcript(renamed)
	write(renamed, "dev-project-2f", now.Add(-time.Hour))
	sidecar(renamed, "debug the dns thing", now.Add(-time.Minute))
	if got := LiveName(renamed); got != "debug the dns thing" {
		t.Errorf("stale registry beat a newer rename: got %q", got)
	}

	// registry is the newer signal (renamed, then renamed again in-CLI)
	fresh := "22222222-2222-4222-8222-222222222222"
	transcript(fresh)
	sidecar(fresh, "old title", now.Add(-time.Hour))
	write(fresh, "current title", now.Add(-time.Minute))
	if got := LiveName(fresh); got != "current title" {
		t.Errorf("older sidecar beat the live registry: got %q", got)
	}

	// no sidecar at all: the registry still names it
	only := "33333333-3333-4333-8333-333333333333"
	transcript(only)
	write(only, "registry only", now)
	if got := LiveName(only); got != "registry only" {
		t.Errorf("registry-only name lost: got %q", got)
	}

	// nothing live and nothing on disk
	if got := LiveName("44444444-4444-4444-8444-444444444444"); got != "" {
		t.Errorf("unknown session named %q", got)
	}
}

// Claude Code labels an unnamed agent after its working directory or its
// session id. Those labels must never be taken for titles: the desk treats a
// live name as authoritative, so one pinned a session to it forever, and a
// session could sit on the desk as a bare hex blob while its own transcript
// held a real title.
func TestPlaceholderLabelsAreNotNames(t *testing.T) {
	sid := "52889ff1-d695-4509-b684-5e986441ff28"
	for _, c := range []struct {
		name, cwd string
		want      bool
	}{
		{"52889ff1", "/Users/dev/GitHub", true},     // the id's first block
		{"52889ff1-2f", "/Users/dev/GitHub", true},  // ... with a hex tail
		{"tal-lu-2f", "/Users/tal.lu", true},        // cwd slug plus tail
		{"tal-lu", "/Users/tal.lu", true},           // cwd slug alone
		{"github-5e", "/Users/tal.lu/GitHub", true}, // case-folded slug
		{"GitHub", "/Users/tal.lu/GitHub", true},    // and as typed
		{"speed-up terraform workflows", "/Users/tal.lu/GitHub", false},
		{"local-dns-failed-apply", "/Users/tal.lu", false},
		{"debug-nimbus-cloudflared-dns", "/Users/tal.lu", false},
		{"52889", "/Users/dev/x", false}, // a real, short, name
		{"", "/Users/dev/x", false},
	} {
		if got := placeholderLabel(c.name, sid, c.cwd); got != c.want {
			t.Errorf("placeholderLabel(%q, cwd=%q) = %v, want %v", c.name, c.cwd, got, c.want)
		}
	}
}

// End to end through the registry: a placeholder label loses to the title the
// session carries, and to nothing at all when it has no title.
func TestLiveNameIgnoresPlaceholderLabels(t *testing.T) {
	root, reg := t.TempDir(), t.TempDir()
	t.Setenv("AGENTBOSS_CLAUDE_PROJECTS", root)
	t.Setenv("AGENTBOSS_CLAUDE_SESSIONS", reg)
	proj := filepath.Join(root, "-Users-tal-lu")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	sid := "0d8d54c8-4ee3-4e1c-b0f3-a63084988007"
	if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The label is published NEWER than the title, which is exactly the case
	// that broke: a live process keeps republishing its own label.
	rec := fmt.Sprintf(`{"sessionId":%q,"name":"tal-lu-2f","cwd":"/Users/tal.lu","pid":%d,"updatedAt":%d}`,
		sid, os.Getpid(), time.Now().UnixMilli())
	if err := os.WriteFile(filepath.Join(reg, "1.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LiveName(sid); got != "" {
		t.Errorf("with no title, a placeholder label should name nothing, got %q", got)
	}
	dir := filepath.Join(proj, sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	title := filepath.Join(dir, "custom-title.json")
	if err := os.WriteFile(title, []byte(`{"customTitle":"debug-nimbus-cloudflared-dns"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	older := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(title, older, older); err != nil {
		t.Fatal(err)
	}
	if got := LiveName(sid); got != "debug-nimbus-cloudflared-dns" {
		t.Errorf("a placeholder label beat the session's own title: got %q", got)
	}
}

// A transcript can open with a long preamble of metadata and file-history
// snapshots before its first real record. One did, pushing its cwd past the
// window peek used to read, so the import picker skipped the whole
// conversation as unrecognizable and there was no way to get it back onto the
// desk.
func TestScanFindsAConversationWithALongPreamble(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBOSS_CLAUDE_PROJECTS", root)
	proj := filepath.Join(root, "-Users-dev-work")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`{"type":"custom-title","customTitle":"the real thread"}` + "\n")
	// 200 lines of preamble: well past any fixed head window.
	for i := 0; i < 200; i++ {
		b.WriteString(`{"type":"file-history-snapshot","isSnapshotUpdate":true}` + "\n")
	}
	b.WriteString(`{"type":"user","cwd":"/Users/dev/work","timestamp":"2026-09-07T18:04:54.107Z",` +
		`"message":{"role":"user","content":"pick up where we left off"}}` + "\n")
	sid := "d365dd31-c9d5-418f-a685-4043e23cef98"
	if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Scan(map[string]bool{}, 100)
	if len(got) != 1 {
		t.Fatalf("scan returned %d conversations, want the one with a preamble", len(got))
	}
	if got[0].SessionID != sid || got[0].Dir != "/Users/dev/work" || got[0].Title != "the real thread" {
		t.Errorf("got %+v", got[0])
	}

	// A file that never names a folder is still not a conversation.
	junk := filepath.Join(proj, "11111111-1111-4111-8111-111111111111.jsonl")
	if err := os.WriteFile(junk, []byte(`{"type":"custom-title","customTitle":"no folder"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Scan(map[string]bool{}, 100); len(got) != 1 {
		t.Errorf("a transcript with no folder should be skipped, got %d", len(got))
	}
}
