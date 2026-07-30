package status

import (
	"testing"
)

func TestApplyLifecycle(t *testing.T) {
	var r Runtime
	steps := []struct {
		event string
		msg   string
		want  Kind
	}{
		{"SessionStart", "", Idle},
		{"UserPromptSubmit", "", Working},
		{"PreToolUse", "", Working},
		{"Notification", "Claude needs your permission to use Bash", NeedsYou},
		{"PostToolUse", "", Working},
		{"Notification", "Claude is waiting for your input", Attention},
		{"Stop", "", Attention},
		{"SessionEnd", "", Idle},
	}
	for _, s := range steps {
		var ok bool
		r, ok = Apply(r, HookEvent{HookEventName: s.event, SessionID: "abc", Message: s.msg})
		if !ok {
			t.Fatalf("%s: expected a write", s.event)
		}
		if r.Status != s.want {
			t.Fatalf("%s: got %s want %s", s.event, r.Status, s.want)
		}
		if r.ClaudeSessionID != "abc" {
			t.Fatalf("%s: claude session id not captured", s.event)
		}
	}
}

func TestApplyUnknownEventIgnored(t *testing.T) {
	prev := Runtime{Status: Working, ClaudeSessionID: "x"}
	got, ok := Apply(prev, HookEvent{HookEventName: "SubagentStop", SessionID: "y"})
	if ok {
		t.Fatal("unknown events must not trigger writes")
	}
	if got.Status != Working || got.ClaudeSessionID != "x" {
		t.Fatalf("unknown event mutated runtime: %+v", got)
	}
}

func TestNotificationKeepsMessage(t *testing.T) {
	r, _ := Apply(Runtime{}, HookEvent{HookEventName: "Notification", Message: "Claude needs your permission to use Bash"})
	if r.Message == "" {
		t.Fatal("message lost")
	}
	if r.Status != NeedsYou {
		t.Fatal("permission notifications must be needs-you")
	}
	r, _ = Apply(r, HookEvent{HookEventName: "PostToolUse"})
	if r.Message != "" {
		t.Fatal("message should clear once work resumes")
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, "s_1", Runtime{Status: Attention, ClaudeSessionID: "cs"}); err != nil {
		t.Fatal(err)
	}
	r := Read(dir, "s_1")
	if r.Status != Attention || r.ClaudeSessionID != "cs" || r.UpdatedAt.IsZero() {
		t.Fatalf("round trip lost data: %+v", r)
	}
	all := ReadAll(dir)
	if len(all) != 1 || all["s_1"].Status != Attention {
		t.Fatalf("ReadAll wrong: %+v", all)
	}
}

func TestReadMissing(t *testing.T) {
	r := Read(t.TempDir(), "nope")
	if r.Status != "" {
		t.Fatalf("expected zero runtime, got %+v", r)
	}
}
