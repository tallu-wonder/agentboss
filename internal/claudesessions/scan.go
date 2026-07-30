// Package claudesessions discovers past Claude Code conversations on disk
// (~/.claude/projects/<munged-cwd>/<uuid>.jsonl) so they can be added to the
// desk and resumed.
package claudesessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tallu-wonder/agentdeck/internal/sanitize"
)

// Conversation is one resumable Claude Code session found on disk.
type Conversation struct {
	SessionID string
	Dir       string // working directory of the conversation
	Title     string // explicit name > summary > first user message
	MTime     time.Time

	custom string    // explicit session name found in the head, if any
	born   time.Time // first timestamp in the transcript
}

// projectsDir returns Claude Code's conversation store.
func projectsDir() string {
	if d := os.Getenv("AGENTDECK_CLAUDE_PROJECTS"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// Scan lists conversations, newest first, skipping session IDs in exclude.
// Only the head of each transcript is read, so scanning hundreds of
// conversations stays fast.
func Scan(exclude map[string]bool, limit int) []Conversation {
	root := projectsDir()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Conversation
	for _, proj := range entries {
		if !proj.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, proj.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if !strings.HasSuffix(name, ".jsonl") || f.IsDir() {
				continue
			}
			sid := strings.TrimSuffix(name, ".jsonl")
			if exclude[sid] {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			p := filepath.Join(root, proj.Name(), name)
			c := peek(p)
			if c.Dir == "" {
				continue // not a conversation transcript we understand
			}
			// Names and late summaries are APPENDED to transcripts:
			// the head alone shows first-prompt fragments, not the
			// name claude's own badge shows.
			ti := TailScan(p)
			switch {
			case ti.Name != "":
				c.Title = ti.Name
			case c.custom != "":
				c.Title = c.custom
			case c.Title == "" && ti.Summary != "":
				c.Title = ti.Summary
			}
			if c.Title == "" {
				continue // no name, no summary, no real message: noise
			}
			c.SessionID = sid
			c.MTime = info.ModTime()
			out = append(out, c)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].MTime.After(out[b].MTime) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// TranscriptPath locates the transcript file for a Claude session ID by
// checking each project directory (the cwd-mangling scheme isn't guessed).
func TranscriptPath(sessionID string) string {
	root := projectsDir()
	if root == "" || sessionID == "" {
		return ""
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name(), sessionID+".jsonl")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// ScratchDir returns the directory where a Claude session keeps its own files:
// its scratchpad if it has one, otherwise the session directory itself. "" if
// the session never made one (or /tmp has since been cleaned).
//
// The session directory is grouped under a mangled form of the launch cwd, so
// like TranscriptPath this searches for the session ID rather than trying to
// reproduce the mangling — and it stays correct if the session was started
// somewhere other than the folder the desk recorded.
// scratchRoot is where Claude Code groups its per-session working directories:
//
//	/tmp/claude-<uid>/<mangled-cwd>/<session-id>/scratchpad
func scratchRoot() string {
	if d := os.Getenv("AGENTDECK_CLAUDE_SCRATCH"); d != "" {
		return d
	}
	return filepath.Join("/tmp", "claude-"+strconv.Itoa(os.Getuid()))
}

func ScratchDir(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	root := scratchRoot()
	// The default root lives under /tmp, which any local user can create
	// entries in. If it isn't ours, a stranger could plant directories (or
	// symlinks) that agentdeck would then hand to the file manager.
	if fi, err := os.Stat(root); err != nil || !ownedByUs(fi) {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(root, "*", sessionID))
	if err != nil {
		return ""
	}
	for _, dir := range matches {
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			continue
		}
		if scratch := filepath.Join(dir, "scratchpad"); isDir(scratch) {
			return scratch
		}
		return dir
	}
	return ""
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// ownedByUs reports whether a directory belongs to the current user.
func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return true // unknown platform: don't break the feature over it
	}
	return fi.IsDir() && int(st.Uid) == os.Getuid()
}

// TailInfo is what the tail of a transcript reveals: the newest explicit
// session name and summary, plus context size, model, and todo progress as
// of the last assistant turn.
type TailInfo struct {
	Name          string
	Summary       string
	ContextTokens int
	Model         string // e.g. "claude-opus-4-8-..."
	TodosDone     int
	TodosTotal    int
	CurrentTodo   string    // the in-progress item, if any
	Born          time.Time // when the claude conversation actually started
}

// Title returns the best human title for a transcript, in priority order:
// the newest explicit session name (custom-title / agent-name lines — what
// Claude Code shows in its own UI and `/rename` sets), then the newest
// summary, then the first user message.
func Title(path string) string {
	t, _ := Probe(path)
	return t
}

// Probe reads a transcript once and returns its best title plus everything
// the tail reveals (context size, model, todos). After Probe, info.Name is
// non-empty exactly when the title is EXPLICIT (a rename record, not a
// derived summary/first-message fallback).
func Probe(path string) (title string, info TailInfo) {
	c := peek(path)
	info = TailScan(path)
	info.Born = c.born
	if info.Name != "" {
		return info.Name, info
	}
	switch {
	case c.custom != "":
		title = c.custom
		info.Name = c.custom // explicit: found in the head
	case info.Summary != "":
		title = info.Summary
	default:
		title = c.Title
	}
	return title, info
}

// TailScan scans the last part of the transcript; names, summaries and
// usage records are appended as a conversation evolves, so the last of each
// wins.
func TailScan(path string) TailInfo {
	var ti TailInfo
	f, err := os.Open(path)
	if err != nil {
		return ti
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ti
	}
	const window = 256 * 1024
	off := st.Size() - window
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && len(buf) > 0 {
		return ti
	}
	lines := strings.Split(string(buf), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // drop the partial first line
	}
	for _, ln := range lines {
		// Cheap pre-filter: only records that can carry something we read are
		// worth unmarshalling. compactMetadata belongs here too — a compaction
		// carries no usage field, and skipping it left the context size stuck
		// at the pre-compact number until the next turn.
		if !strings.Contains(ln, `"summary"`) &&
			!strings.Contains(ln, `"customTitle"`) &&
			!strings.Contains(ln, `"agentName"`) &&
			!strings.Contains(ln, `"usage"`) &&
			!strings.Contains(ln, `"compactMetadata"`) {
			continue
		}
		var l line
		if json.Unmarshal([]byte(ln), &l) != nil {
			continue
		}
		switch {
		case l.Type == "custom-title" && l.CustomTitle != "":
			ti.Name = clean(l.CustomTitle)
		case l.Type == "agent-name" && l.AgentName != "":
			ti.Name = clean(l.AgentName)
		case l.Type == "summary" && l.Summary != "":
			ti.Summary = clean(l.Summary)
		}
		// A compaction resets the context without producing a usage record.
		// Records are processed in order, so taking postTokens here lets a
		// later turn override it, and a compaction override an earlier turn.
		if l.Type == "system" && l.Subtype == "compact_boundary" && l.CompactMetadata != nil {
			if n := l.CompactMetadata.PostTokens; n > 0 {
				ti.ContextTokens = n
			}
		}
		// Subagent (sidechain) turns run their own models with their own
		// contexts — they must not pollute the main session's stats.
		if l.Message != nil && !l.IsSidechain {
			if u := l.Message.Usage; u != nil {
				if n := u.InputTokens + u.CacheCreation + u.CacheRead + u.OutputTokens; n > 0 {
					ti.ContextTokens = n
				}
				// Keep the last REAL model: synthetic/internal pseudo-models
				// ("<synthetic>") must not erase it.
				if mo := l.Message.Model; mo != "" && mo[0] != '<' {
					ti.Model = sanitize.Line(mo)
				}
			}
			if strings.Contains(ln, `"TodoWrite"`) {
				if done, total, cur, ok := parseTodos(l.Message.Content); ok {
					ti.TodosDone, ti.TodosTotal, ti.CurrentTodo = done, total, cur
				}
			}
		}
	}
	return ti
}

// parseTodos extracts todo progress from a TodoWrite tool call's content.
func parseTodos(raw json.RawMessage) (done, total int, current string, ok bool) {
	var blocks []struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Input struct {
			Todos []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"todos"`
		} `json:"input"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return 0, 0, "", false
	}
	for _, b := range blocks {
		if b.Type != "tool_use" || b.Name != "TodoWrite" || len(b.Input.Todos) == 0 {
			continue
		}
		done, total, current, ok = 0, len(b.Input.Todos), "", true
		for _, t := range b.Input.Todos {
			switch t.Status {
			case "completed":
				done++
			case "in_progress":
				current = t.Content
			}
		}
	}
	return done, total, current, ok
}

// Family extracts the model family from a model id: claude-opus-4-8-… →
// "opus". Synthetic pseudo-models yield "".
func Family(model string) string {
	s := strings.TrimPrefix(model, "claude-")
	if s == "" || !(s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z') {
		return ""
	}
	return strings.ToLower(strings.SplitN(s, "-", 2)[0])
}

// builtinPrices is USD per MTok (input, output) per model family, as published
// in 2026-06. Rates change, and a hardcoded table quietly turns the cost column
// into fiction, so they can be overridden — see priceTable.
var builtinPrices = map[string][2]float64{
	"fable":  {10, 50},
	"mythos": {10, 50},
	"opus":   {5, 25},
	"sonnet": {3, 15},
	"haiku":  {1, 5},
}

// unknownFamilyPrice is what an unrecognized model is billed at: the top tier,
// so a new model understates nothing.
var unknownFamilyPrice = [2]float64{5, 25}

var (
	priceOnce  sync.Once
	priceTable map[string][2]float64
)

// prices returns the effective table: a JSON file of {"family": [in, out]} per
// MTok, from AGENTDECK_PRICING or ~/.agentdeck/pricing.json, merged over the
// built-in rates. Anything unparseable is ignored in favour of the built-ins —
// a broken price file must not stop the desk from working.
func prices() map[string][2]float64 {
	priceOnce.Do(func() {
		priceTable = map[string][2]float64{}
		for k, v := range builtinPrices {
			priceTable[k] = v
		}
		path := os.Getenv("AGENTDECK_PRICING")
		if path == "" {
			path = filepath.Join(agentdeckHome(), "pricing.json")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var custom map[string][2]float64
		if json.Unmarshal(data, &custom) != nil {
			return
		}
		for k, v := range custom {
			if v[0] >= 0 && v[1] >= 0 {
				priceTable[strings.ToLower(k)] = v
			}
		}
	})
	return priceTable
}

// agentdeckHome mirrors paths.Home() without importing it, keeping this package
// free of a dependency it otherwise has no use for.
func agentdeckHome() string {
	if v := os.Getenv("AGENTDECK_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".agentdeck")
}

// price returns USD per MTok (input, output) for a model family. Cache writes
// bill 1.25x input (5m TTL) and cache reads 0.1x; those multipliers are part of
// the API's pricing model rather than per-family rates, so they are applied at
// the call site.
func price(family string) (in, out float64) {
	if p, ok := prices()[family]; ok {
		return p[0], p[1]
	}
	return unknownFamilyPrice[0], unknownFamilyPrice[1]
}

// CostDelta sums the estimated USD cost of usage records in a transcript
// starting at byte offset from. It returns the cost of the newly read span
// and the new offset. If the file shrank (replaced), it rescans from the
// start and reports rescanned=true — the caller should reset its total.
func CostDelta(path string, from int64) (delta float64, newOff int64, rescanned bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, from, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, from, false
	}
	if st.Size() < from {
		from = 0
		rescanned = true
	}
	if _, err := f.Seek(from, 0); err != nil {
		return 0, from, rescanned
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	newOff = from
	for sc.Scan() {
		ln := sc.Bytes()
		newOff += int64(len(ln)) + 1
		if !strings.Contains(string(ln), `"usage"`) {
			continue
		}
		var l line
		if json.Unmarshal(ln, &l) != nil || l.Message == nil || l.Message.Usage == nil {
			continue
		}
		u := l.Message.Usage
		pin, pout := price(Family(l.Message.Model))
		delta += (float64(u.InputTokens)*pin +
			float64(u.CacheCreation)*1.25*pin +
			float64(u.CacheRead)*0.1*pin) / 1e6
		delta += float64(u.OutputTokens) * pout / 1e6
	}
	return delta, newOff, rescanned
}

// LiveNames returns the current name of every RUNNING Claude session, keyed
// by session ID — read from Claude Code's live-session registry
// (~/.claude/sessions/<pid>.json), which is exactly the name shown in
// claude's own status badge. Entries whose process is gone are ignored.
func LiveNames() map[string]string {
	dir := os.Getenv("AGENTDECK_CLAUDE_SESSIONS")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		dir = filepath.Join(home, ".claude", "sessions")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type reg struct {
		SessionID string `json:"sessionId"`
		Name      string `json:"name"`
		PID       int    `json:"pid"`
		UpdatedAt int64  `json:"updatedAt"`
	}
	newest := map[string]reg{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r reg
		if json.Unmarshal(data, &r) != nil || r.SessionID == "" || r.Name == "" {
			continue
		}
		if r.PID > 0 && !pidAlive(r.PID) {
			continue // stale registry file
		}
		if prev, ok := newest[r.SessionID]; !ok || r.UpdatedAt > prev.UpdatedAt {
			newest[r.SessionID] = r
		}
	}
	out := map[string]string{}
	for sid, r := range newest {
		out[sid] = clean(r.Name)
	}
	return out
}

func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// AppendRename records a new session name in the transcript — the same
// custom-title / agent-name records Claude Code itself writes on /rename —
// so the name survives resume and shows up in claude's own pickers.
func AppendRename(sessionID, name string) error {
	p := TranscriptPath(sessionID)
	if p == "" {
		return os.ErrNotExist
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	rec := func(typ, key string) []byte {
		b, _ := json.Marshal(map[string]string{"type": typ, key: name, "sessionId": sessionID})
		return append(b, '\n')
	}
	buf := append(rec("custom-title", "customTitle"), rec("agent-name", "agentName")...)
	_, err = f.Write(buf)
	return err
}

// transcript line shapes we care about (everything else is ignored).
type line struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	Summary     string `json:"summary"`
	CustomTitle string `json:"customTitle"`
	AgentName   string `json:"agentName"`
	CWD         string `json:"cwd"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	// Written when a conversation is compacted; postTokens is the size of the
	// context that survived.
	CompactMetadata *struct {
		Trigger    string `json:"trigger"`
		PreTokens  int    `json:"preTokens"`
		PostTokens int    `json:"postTokens"`
	} `json:"compactMetadata"`
	Message *struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *usage          `json:"usage"`
	} `json:"message"`
}

type usage struct {
	InputTokens   int `json:"input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
	OutputTokens  int `json:"output_tokens"`
}

// peek reads the head of a transcript to extract cwd, any explicit session
// name, and a fallback title (summary, else first user message).
func peek(path string) Conversation {
	var c Conversation
	f, err := os.Open(path)
	if err != nil {
		return c
	}
	defer f.Close()
	var summary, firstUser string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 256*1024)
	for n := 0; sc.Scan() && n < 80; n++ {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		switch {
		case l.Type == "custom-title" && l.CustomTitle != "":
			c.custom = clean(l.CustomTitle) // last one wins
		case l.Type == "agent-name" && l.AgentName != "":
			c.custom = clean(l.AgentName)
		case l.Type == "summary" && l.Summary != "" && summary == "":
			summary = clean(l.Summary)
		}
		if l.CWD != "" && c.Dir == "" {
			c.Dir = l.CWD
		}
		if c.born.IsZero() && l.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, l.Timestamp); err == nil {
				c.born = t
			}
		}
		if firstUser == "" && l.Type == "user" && l.Message != nil {
			if t := firstText(l.Message.Content); t != "" && !junkText(t) {
				firstUser = clean(t)
			}
		}
	}
	c.Title = summary
	if c.Title == "" {
		c.Title = firstUser
	}
	return c
}

// firstText extracts readable text from a message content field, which is
// either a plain string or a list of typed blocks.
func firstText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return b.Text
			}
		}
	}
	return ""
}

// junkText reports tool/system noise that makes a useless title: local
// command caveats, IDE notifications, and other XMLish wrapper blocks.
func junkText(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "<") || strings.HasPrefix(s, "Caveat:")
}

// clean collapses whitespace and truncates to a title-sized string.
func clean(s string) string {
	// Transcript text is agent-authored: strip anything a terminal or tmux
	// would interpret before it becomes a name on screen.
	s = sanitize.Line(s)
	// Command-style openings ("<command-name>/foo</command-name> …") aren't
	// useful titles; keep them but strip XMLish noise cheaply.
	s = strings.NewReplacer("<command-name>", "", "</command-name>", "").Replace(s)
	r := []rune(s)
	if len(r) > 60 {
		return string(r[:59]) + "…"
	}
	return s
}
