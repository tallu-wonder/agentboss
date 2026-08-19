// Package codexsessions reads Codex CLI rollout transcripts
// (~/.codex/sessions/<y>/<m>/<d>/rollout-<ts>-<uuid>.jsonl) so Codex
// conversations can be listed, named, resumed, and monitored on the desk.
//
// Unlike Claude Code, Codex records its context window and a cumulative
// token total directly in the transcript, and its status transitions
// (task_started / task_complete / turn_aborted) are transcript events — so a
// Codex session's live state is derivable without any hook.
package codexsessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tallu-wonder/agentboss/internal/paths"
	"github.com/tallu-wonder/agentboss/internal/sanitize"
)

// Conversation is one resumable Codex session found on disk.
type Conversation struct {
	SessionID string
	Dir       string
	Title     string
	MTime     time.Time
}

// Info is what a transcript reveals about a Codex session.
type Info struct {
	Title         string
	TitleExplicit bool // came from a thread_name_updated record
	Dir           string
	Model         string
	ContextTokens int // context size of the last turn
	ContextWindow int // the model's real window, as Codex reports it
	TotalTokens   int // cumulative across the session
	Born          time.Time
	// Phase is the last lifecycle event seen: "working", "done", "aborted"
	// or "" when the transcript says nothing.
	Phase string
}

// ---- transcript line shapes (everything else is ignored) ----------------

type line struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type metaPayload struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	Timestamp string `json:"timestamp"`
}

type turnCtxPayload struct {
	Model string `json:"model"`
	CWD   string `json:"cwd"`
}

type tokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	CachedInput  int `json:"cached_input_tokens"`
	CacheWrite   int `json:"cache_write_input_tokens"`
	OutputTokens int `json:"output_tokens"`
	Reasoning    int `json:"reasoning_output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type eventPayload struct {
	Type       string `json:"type"`
	ThreadName string `json:"thread_name"`
	Message    string `json:"message"`
	Info       *struct {
		TotalTokenUsage    tokenUsage `json:"total_token_usage"`
		LastTokenUsage     tokenUsage `json:"last_token_usage"`
		ModelContextWindow int        `json:"model_context_window"`
	} `json:"info"`
}

type respItemPayload struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// ---- names -------------------------------------------------------------

// indexEntry is one line of Codex's session_index.jsonl.
type indexEntry struct {
	ID         string `json:"id"`
	ThreadName string `json:"thread_name"`
	UpdatedAt  string `json:"updated_at"`
}

// Names returns the current name of every Codex thread, from Codex's own
// append-only index. The index is last-write-wins, so later lines override
// earlier ones — this is where an in-CLI rename lands, and what the resume
// picker displays.
func Names() map[string]string {
	f, err := os.Open(paths.CodexSessionIndex())
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e indexEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.ID == "" {
			continue
		}
		if e.ThreadName == "" {
			delete(out, e.ID) // a cleared name reverts to derived titles
			continue
		}
		out[e.ID] = clean(e.ThreadName)
	}
	return out
}

// SetName records a new name for a thread by appending to Codex's index in
// its own format, so the name shows up in `codex resume` too.
func SetName(threadID, name string) error {
	if threadID == "" || name == "" {
		return os.ErrInvalid
	}
	line, err := json.Marshal(indexEntry{
		ID:         threadID,
		ThreadName: name,
		UpdatedAt:  time.Now().UTC().Format("2006-01-02T15:04:05.000000Z"),
	})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(paths.CodexSessionIndex(),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// ---- discovery ---------------------------------------------------------

// TranscriptPath locates the live rollout for a Codex thread.
//
// Resuming a thread starts a NEW rollout file whose name carries a fresh UUID
// while session_meta keeps the original thread ID, so matching on the file
// name alone finds the abandoned original and reports stale state. This
// matches on the thread ID recorded inside and returns the newest such file.
func TranscriptPath(threadID string) string {
	if threadID == "" {
		return ""
	}
	best, bestMod := "", time.Time{}
	_ = filepath.WalkDir(paths.CodexSessionsDir(), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil //nolint:nilerr // unreadable dirs are skipped, not fatal
		}
		fi, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr
		}
		// Cheap pre-filter: the thread ID appears in the file name unless this
		// rollout is a resume, in which case we must read the header.
		if !strings.Contains(filepath.Base(p), threadID) {
			if h := peekMeta(p); h.sessionID != threadID {
				return nil
			}
		}
		if fi.ModTime().After(bestMod) {
			best, bestMod = p, fi.ModTime()
		}
		return nil
	})
	return best
}

// Scan lists Codex conversations, newest first, skipping IDs in exclude.
func Scan(exclude map[string]bool, limit int) []Conversation {
	root := paths.CodexSessionsDir()
	var out []Conversation
	seen := map[string]time.Time{} // newest rollout per thread
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil //nolint:nilerr
		}
		fi, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr
		}
		// The thread ID inside is authoritative: a resumed thread has several
		// rollouts, and only the newest reflects its current state.
		head := peekMeta(p)
		sid := head.sessionID
		if sid == "" {
			sid = sessionIDFromName(filepath.Base(p))
		}
		if sid == "" || exclude[sid] {
			return nil
		}
		if prev, ok := seen[sid]; ok && !fi.ModTime().After(prev) {
			return nil // an older rollout of a thread we already have
		}
		info := Probe(p)
		if info.Dir == "" || info.Title == "" {
			return nil // not a transcript we can place or label
		}
		seen[sid] = fi.ModTime()
		out = append(out, Conversation{
			SessionID: sid,
			Dir:       info.Dir,
			Title:     info.Title,
			MTime:     fi.ModTime(),
		})
		return nil
	})
	// A newer rollout can be walked after an older one; keep one row per
	// thread, the newest.
	best := map[string]Conversation{}
	for _, c := range out {
		if prev, ok := best[c.SessionID]; !ok || c.MTime.After(prev.MTime) {
			best[c.SessionID] = c
		}
	}
	out = out[:0]
	for _, c := range best {
		out = append(out, c)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].MTime.After(out[b].MTime) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// sessionIDFromName pulls the UUID out of "rollout-<timestamp>-<uuid>.jsonl".
func sessionIDFromName(name string) string {
	if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
		return ""
	}
	base := strings.TrimSuffix(strings.TrimPrefix(name, "rollout-"), ".jsonl")
	// The UUID is the last five dash-separated groups.
	parts := strings.Split(base, "-")
	if len(parts) < 5 {
		return ""
	}
	return strings.Join(parts[len(parts)-5:], "-")
}

// ---- probing -----------------------------------------------------------

// Probe reads a transcript and returns its title, model, token state and
// lifecycle phase. The head is read for metadata, the tail for the newest
// name/usage/status — the same two-window approach used for Claude.
func Probe(path string) Info {
	var info Info
	f, err := os.Open(path)
	if err != nil {
		return info
	}
	defer f.Close()

	// --- head: session_meta (cwd, id, birth) + first user message ---
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 4*1024*1024)
	firstUser := ""
	for n := 0; sc.Scan() && n < 60; n++ {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		switch l.Type {
		case "session_meta":
			var m metaPayload
			if json.Unmarshal(l.Payload, &m) == nil {
				if info.Dir == "" {
					info.Dir = m.CWD
				}
				if t, err := time.Parse(time.RFC3339, m.Timestamp); err == nil {
					info.Born = t
				}
			}
		case "turn_context":
			var tc turnCtxPayload
			if json.Unmarshal(l.Payload, &tc) == nil && info.Dir == "" {
				info.Dir = tc.CWD
			}
		case "event_msg":
			var e eventPayload
			if json.Unmarshal(l.Payload, &e) == nil && e.Type == "user_message" && firstUser == "" {
				firstUser = clean(firstHumanLine(e.Message))
			}
		case "response_item":
			var r respItemPayload
			if json.Unmarshal(l.Payload, &r) == nil && r.Type == "message" && r.Role == "user" && firstUser == "" {
				for _, c := range r.Content {
					if t := clean(firstHumanLine(c.Text)); t != "" {
						firstUser = t
						break
					}
				}
			}
		}
		if info.Born.IsZero() && l.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, l.Timestamp); err == nil {
				info.Born = t
			}
		}
	}

	// --- tail: newest name, usage, model, phase ---
	tail := tailScan(path)
	info.Model = tail.Model
	info.ContextTokens = tail.ContextTokens
	info.ContextWindow = tail.ContextWindow
	info.TotalTokens = tail.TotalTokens
	info.Phase = tail.Phase
	switch {
	case tail.Title != "":
		info.Title = tail.Title
		info.TitleExplicit = true
	case firstUser != "":
		info.Title = firstUser
	case info.Dir != "":
		info.Title = filepath.Base(info.Dir)
	}
	return info
}

// tailScan reads the last window of a transcript, where the newest name,
// usage and lifecycle records live.
func tailScan(path string) Info {
	var info Info
	f, err := os.Open(path)
	if err != nil {
		return info
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return info
	}
	const window = 512 * 1024
	off := st.Size() - window
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && len(buf) > 0 {
		return info
	}
	lines := strings.Split(string(buf), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // drop the partial first line
	}
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		var l line
		if json.Unmarshal([]byte(ln), &l) != nil {
			continue
		}
		switch l.Type {
		case "turn_context":
			var tc turnCtxPayload
			if json.Unmarshal(l.Payload, &tc) == nil && tc.Model != "" {
				info.Model = sanitize.Line(tc.Model)
			}
		case "event_msg":
			var e eventPayload
			if json.Unmarshal(l.Payload, &e) != nil {
				continue
			}
			switch e.Type {
			case "thread_name_updated":
				if e.ThreadName != "" {
					info.Title = clean(e.ThreadName)
				}
			case "token_count":
				if e.Info != nil {
					if w := e.Info.ModelContextWindow; w > 0 {
						info.ContextWindow = w
					}
					// Context size is the last turn's prompt. cached_input is a
					// SUBSET of input (Codex reports total = input + output), so
					// adding it would double-count the cached prefix.
					if n := e.Info.LastTokenUsage.InputTokens; n > 0 {
						info.ContextTokens = n
					}
					if t := e.Info.TotalTokenUsage.TotalTokens; t > 0 {
						info.TotalTokens = t
					}
				}
			case "task_started":
				info.Phase = "working"
			case "task_complete":
				info.Phase = "done"
			case "turn_aborted":
				info.Phase = "aborted"
			}
		}
	}
	return info
}

// firstHumanLine extracts a title from a user message, skipping the context
// blocks editors and AGENTS.md injections prepend: IDE context headers,
// markdown headings, XML-ish instruction wrappers, @file references. Returns
// "" when the whole message is machinery, so the caller keeps looking.
func firstHumanLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || junkLine(ln) {
			continue
		}
		return clean(ln)
	}
	return ""
}

// junkLine reports a line that is machinery rather than something a person
// typed as their request.
func junkLine(s string) bool {
	switch s[0] {
	case '#', '<', '@', '`', '|', '-', '*':
		return true
	}
	return strings.HasPrefix(s, "Caveat:") || strings.HasPrefix(s, "</")
}

// clean collapses whitespace, strips control characters, and truncates to a
// title-sized string.
func clean(s string) string {
	// Rollout text is agent-authored: strip anything a terminal or tmux would
	// interpret before it becomes a name on screen.
	s = sanitize.Line(s)
	r := []rune(s)
	if len(r) > 60 {
		return string(r[:59]) + "…"
	}
	return s
}

// FindLatest locates the transcript Codex created for a session agentboss
// launched in dir at (or after) notBefore, returning its session ID and path.
//
// Codex only reveals a thread ID once a turn completes (via notify), but it
// writes the transcript as soon as the session starts. Matching on the
// working directory plus a start time lets the desk adopt a running Codex
// session immediately — name, model, tokens and status all follow from it —
// instead of showing a bare folder name until the first turn ends.
func FindLatest(dir string, notBefore time.Time) (sessionID, path string) {
	root := paths.CodexSessionsDir()
	// A small grace absorbs clock skew between our wake and Codex's first write.
	cutoff := notBefore.Add(-30 * time.Second)
	var newest time.Time
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil //nolint:nilerr
		}
		fi, err := d.Info()
		if err != nil || fi.ModTime().Before(cutoff) {
			return nil //nolint:nilerr // cheap filter before parsing anything
		}
		head := peekMeta(p)
		if head.cwd != dir || head.born.Before(cutoff) {
			return nil
		}
		sid := head.sessionID
		if sid == "" {
			sid = sessionIDFromName(filepath.Base(p))
		}
		if sid == "" {
			return nil
		}
		if head.born.After(newest) {
			newest, sessionID, path = head.born, sid, p
		}
		return nil
	})
	return sessionID, path
}

// metaHead is the little that FindLatest and TranscriptPath need from a
// transcript's first lines.
type metaHead struct {
	sessionID string
	cwd       string
	born      time.Time
}

// peekMeta reads only the session_meta record, so scanning many transcripts
// stays cheap.
func peekMeta(path string) metaHead {
	var h metaHead
	f, err := os.Open(path)
	if err != nil {
		return h
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 0; sc.Scan() && n < 5; n++ {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Type != "session_meta" {
			continue
		}
		var m metaPayload
		if json.Unmarshal(l.Payload, &m) != nil {
			continue
		}
		h.sessionID = m.SessionID
		h.cwd = m.CWD
		if t, err := time.Parse(time.RFC3339, m.Timestamp); err == nil {
			h.born = t
		} else if t, err := time.Parse(time.RFC3339, l.Timestamp); err == nil {
			h.born = t
		}
		return h
	}
	return h
}
