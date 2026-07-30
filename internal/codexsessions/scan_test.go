package codexsessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeRollout writes a transcript in Codex's real on-disk layout:
// <root>/<y>/<m>/<d>/rollout-<timestamp>-<uuid>.jsonl
func writeRollout(t *testing.T, root, sid, body string) string {
	t.Helper()
	dir := filepath.Join(root, "2026", "07", "20")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, fmt.Sprintf("rollout-2026-07-20T19-37-44-%s.jsonl", sid))
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const uuid = "019f8063-fcc4-7e11-bbe5-a9f1d66b0c66"

func TestSessionIDFromName(t *testing.T) {
	got := sessionIDFromName("rollout-2026-07-20T19-37-44-" + uuid + ".jsonl")
	if got != uuid {
		t.Fatalf("sessionIDFromName = %q, want %q", got, uuid)
	}
	if sessionIDFromName("notes.txt") != "" {
		t.Fatal("non-rollout files must yield no id")
	}
}

func TestProbeReadsMetaModelUsageAndName(t *testing.T) {
	root := t.TempDir()
	p := writeRollout(t, root, uuid, `{"timestamp":"2026-07-20T16:38:10.018Z","type":"session_meta","payload":{"session_id":"`+uuid+`","cwd":"/home/dev/projects/checkout","timestamp":"2026-07-20T16:37:44.261Z"}}
{"timestamp":"2026-07-20T16:38:11.000Z","type":"event_msg","payload":{"type":"user_message","message":"check why the login test is flaky"}}
{"timestamp":"2026-07-20T16:38:12.000Z","type":"turn_context","payload":{"cwd":"/home/dev/projects/checkout","model":"gpt-5.6-sol"}}
{"timestamp":"2026-07-20T16:38:20.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":6256768},"last_token_usage":{"input_tokens":75897,"cached_input_tokens":75776,"total_tokens":76022},"model_context_window":258400}}}
{"timestamp":"2026-07-20T16:38:30.000Z","type":"event_msg","payload":{"type":"thread_name_updated","thread_id":"`+uuid+`","thread_name":"Investigate flaky login test"}}
{"timestamp":"2026-07-20T16:38:31.000Z","type":"event_msg","payload":{"type":"task_complete"}}
`)
	info := Probe(p)
	if info.Title != "Investigate flaky login test" || !info.TitleExplicit {
		t.Fatalf("title = %q explicit=%v", info.Title, info.TitleExplicit)
	}
	if info.Dir != "/home/dev/projects/checkout" {
		t.Fatalf("dir = %q", info.Dir)
	}
	if info.Model != "gpt-5.6-sol" {
		t.Fatalf("model = %q", info.Model)
	}
	// Context size is the last turn's input alone (cached_input is a subset of
	// it), not the cumulative total.
	if info.ContextTokens != 75897 {
		t.Fatalf("context tokens = %d, want 75897", info.ContextTokens)
	}
	if info.ContextWindow != 258400 {
		t.Fatalf("context window = %d", info.ContextWindow)
	}
	if info.TotalTokens != 6256768 {
		t.Fatalf("total tokens = %d", info.TotalTokens)
	}
	if info.Phase != "done" {
		t.Fatalf("phase = %q, want done", info.Phase)
	}
	if info.Born.IsZero() {
		t.Fatal("born should come from session_meta")
	}
}

func TestProbeFallsBackToFirstUserMessage(t *testing.T) {
	root := t.TempDir()
	p := writeRollout(t, root, uuid, `{"type":"session_meta","payload":{"session_id":"`+uuid+`","cwd":"/tmp/work","timestamp":"2026-07-20T16:37:44Z"}}
{"type":"event_msg","payload":{"type":"user_message","message":"fix the flaky apply"}}
{"type":"event_msg","payload":{"type":"task_started"}}
`)
	info := Probe(p)
	if info.Title != "fix the flaky apply" || info.TitleExplicit {
		t.Fatalf("title = %q explicit = %v", info.Title, info.TitleExplicit)
	}
	if info.Phase != "working" {
		t.Fatalf("phase = %q, want working", info.Phase)
	}
}

func TestScanAndTranscriptPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTDECK_CODEX_SESSIONS", root)
	want := writeRollout(t, root, uuid, `{"type":"session_meta","payload":{"session_id":"`+uuid+`","cwd":"/tmp/work","timestamp":"2026-07-20T16:37:44Z"}}
{"type":"event_msg","payload":{"type":"user_message","message":"hello there"}}
`)
	if got := TranscriptPath(uuid); got != want {
		t.Fatalf("TranscriptPath = %q, want %q", got, want)
	}
	if TranscriptPath("no-such-id") != "" {
		t.Fatal("unknown id must yield no path")
	}
	got := Scan(map[string]bool{}, 0)
	if len(got) != 1 || got[0].SessionID != uuid || got[0].Title != "hello there" {
		t.Fatalf("Scan = %+v", got)
	}
	if len(Scan(map[string]bool{uuid: true}, 0)) != 0 {
		t.Fatal("exclude ignored")
	}
}

func TestContextTokensDoNotDoubleCountCachedInput(t *testing.T) {
	// Codex reports total = input + output, with cached_input a SUBSET of
	// input. Adding cached would roughly double a cache-heavy session's
	// reported context.
	root := t.TempDir()
	p := writeRollout(t, root, uuid, `{"type":"session_meta","payload":{"session_id":"`+uuid+`","cwd":"/tmp/w","timestamp":"2026-07-20T16:00:00Z"}}
{"type":"event_msg","payload":{"type":"user_message","message":"go"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":6180746},"last_token_usage":{"input_tokens":75897,"cached_input_tokens":75776,"output_tokens":125,"total_tokens":76022},"model_context_window":258400}}}
`)
	info := Probe(p)
	if info.ContextTokens != 75897 {
		t.Fatalf("context tokens = %d, want 75897 (input only)", info.ContextTokens)
	}
}

func TestFindLatestMatchesDirAndStartTime(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTDECK_CODEX_SESSIONS", root)
	mk := func(sid, cwd, born string) string {
		return writeRollout(t, root, sid,
			`{"type":"session_meta","payload":{"session_id":"`+sid+`","cwd":"`+cwd+`","timestamp":"`+born+`"}}
{"type":"event_msg","payload":{"type":"user_message","message":"work"}}
`)
	}
	// An older conversation in the same folder, and one in a different folder.
	mk("019f0000-0000-7000-8000-000000000001", "/tmp/work", "2026-07-01T10:00:00Z")
	mk("019f0000-0000-7000-8000-000000000002", "/tmp/other", "2026-07-20T10:00:00Z")
	wantPath := mk("019f0000-0000-7000-8000-000000000003", "/tmp/work", "2026-07-20T10:00:00Z")

	launched, _ := time.Parse(time.RFC3339, "2026-07-20T09:59:00Z")
	id, path := FindLatest("/tmp/work", launched)
	if id != "019f0000-0000-7000-8000-000000000003" || path != wantPath {
		t.Fatalf("FindLatest = %q, %q", id, path)
	}
	// Nothing started in that folder after this point.
	future, _ := time.Parse(time.RFC3339, "2026-07-25T00:00:00Z")
	if id, _ := FindLatest("/tmp/work", future); id != "" {
		t.Fatalf("expected no match after cutoff, got %q", id)
	}
	// A folder we never launched in.
	if id, _ := FindLatest("/tmp/nowhere", launched); id != "" {
		t.Fatalf("unrelated dir matched: %q", id)
	}
}

func TestTitleSkipsInjectedContextBlocks(t *testing.T) {
	root := t.TempDir()
	// Two shapes a Codex history routinely starts a turn with — an IDE context
	// header and an AGENTS.md instruction wrapper — each followed by the actual
	// request, which is the part worth showing as a title.
	p := writeRollout(t, root, uuid, `{"type":"session_meta","payload":{"session_id":"`+uuid+`","cwd":"/tmp/w","timestamp":"2026-07-20T16:00:00Z"}}
{"type":"event_msg","payload":{"type":"user_message","message":"# Context from my IDE setup:\n\n## Active file: checkout-service/values.yaml"}}
{"type":"event_msg","payload":{"type":"user_message","message":"# AGENTS.md instructions\n\n<INSTRUCTIONS>\n@CONVENTIONS.md\n</INSTRUCTIONS>"}}
{"type":"event_msg","payload":{"type":"user_message","message":"bump the redis chart to 19.x and re-render"}}
`)
	if got := Probe(p).Title; got != "bump the redis chart to 19.x and re-render" {
		t.Fatalf("title = %q, want the real request", got)
	}
}

func TestTitleTakesFirstRealLineOfAMultilinePrompt(t *testing.T) {
	root := t.TempDir()
	p := writeRollout(t, root, uuid, `{"type":"session_meta","payload":{"session_id":"`+uuid+`","cwd":"/tmp/w","timestamp":"2026-07-20T16:00:00Z"}}
{"type":"event_msg","payload":{"type":"user_message","message":"migrate the auth service\n\nsteps:\n- read the plan\n- apply it"}}
`)
	if got := Probe(p).Title; got != "migrate the auth service" {
		t.Fatalf("title = %q, want the first real line only", got)
	}
}

func TestNamesReadsCodexIndexLastWriteWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTDECK_CODEX_HOME", home)
	// Codex's index is append-only: each rename adds a line.
	os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(
		`{"id":"t-1","thread_name":"first try","updated_at":"2026-07-26T10:28:42.576620Z"}
{"id":"t-2","thread_name":"other thread","updated_at":"2026-07-26T10:29:00.000000Z"}
{"id":"t-1","thread_name":"121212","updated_at":"2026-07-26T10:37:33.415355Z"}
{"id":"t-3","thread_name":"named then cleared","updated_at":"2026-07-26T10:30:00.000000Z"}
{"id":"t-3","thread_name":"","updated_at":"2026-07-26T10:31:00.000000Z"}
`), 0o644)
	n := Names()
	if n["t-1"] != "121212" {
		t.Errorf("last write should win: got %q", n["t-1"])
	}
	if n["t-2"] != "other thread" {
		t.Errorf("t-2 = %q", n["t-2"])
	}
	if _, ok := n["t-3"]; ok {
		t.Errorf("a cleared name should fall back to derived titles, got %q", n["t-3"])
	}
}

func TestSetNameAppendsInCodexFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTDECK_CODEX_HOME", home)
	idx := filepath.Join(home, "session_index.jsonl")
	os.WriteFile(idx, []byte(`{"id":"t-1","thread_name":"old","updated_at":"2026-07-26T10:00:00.000000Z"}`+"\n"), 0o644)
	if err := SetName("t-1", "new name"); err != nil {
		t.Fatal(err)
	}
	if got := Names()["t-1"]; got != "new name" {
		t.Fatalf("Names after SetName = %q", got)
	}
	// The original line must survive: Codex's index is a log, not a record.
	body, _ := os.ReadFile(idx)
	if !strings.Contains(string(body), `"old"`) || strings.Count(string(body), "\n") != 2 {
		t.Fatalf("expected an appended line, got:\n%s", body)
	}
}

func TestTranscriptPathPrefersTheLiveRolloutOfAResumedThread(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTDECK_CODEX_SESSIONS", root)
	const thread = "019f5572-442b-7980-aebe-a4a897c69da6"
	// The original rollout: file name carries the thread id.
	old := writeRollout(t, root, thread,
		`{"type":"session_meta","payload":{"session_id":"`+thread+`","cwd":"/tmp/w","timestamp":"2026-07-12T11:29:39Z"}}
{"type":"event_msg","payload":{"type":"user_message","message":"original"}}
`)
	// Resuming writes a NEW file whose name carries a different uuid while
	// session_meta keeps the thread id — this is the live one.
	live := writeRollout(t, root, "019f9dee-ff16-7800-9000-111122223333",
		`{"type":"session_meta","payload":{"session_id":"`+thread+`","cwd":"/tmp/w","timestamp":"2026-07-26T13:18:33Z"}}
{"type":"event_msg","payload":{"type":"user_message","message":"resumed"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":9},"last_token_usage":{"input_tokens":98318,"cached_input_tokens":90000,"output_tokens":9,"total_tokens":98327},"model_context_window":258400}}}
`)
	// Make the resumed rollout the newest.
	past := time.Now().Add(-time.Hour)
	os.Chtimes(old, past, past)
	if got := TranscriptPath(thread); got != live {
		t.Fatalf("TranscriptPath = %q, want the live rollout %q", filepath.Base(got), filepath.Base(live))
	}
	// Scan reports the thread id (not the file name's uuid) and one row only.
	got := Scan(map[string]bool{}, 0)
	if len(got) != 1 || got[0].SessionID != thread {
		t.Fatalf("Scan = %+v, want one row keyed by the thread id", got)
	}
}
