// agentdeck — a persistent work desk for coding-agent sessions.
//
// It manages sessions of more than one agent CLI (Claude Code and Codex) on one
// screen. Every session runs inside its own tmux session (so it survives closed
// terminals and manager restarts); the desk layout lives in a state file (so it
// survives reboots, waking each session with its own agent's resume command);
// live status comes from Claude Code hooks and from Codex's transcript events
// (working / needs you / finished).
//
// Subcommands:
//
//	agentdeck               launch (or jump to) the manager UI inside tmux
//	agentdeck hook          [internal] invoked by Claude Code hooks
//	agentdeck install-hooks (re)install status hooks into Claude settings
//	agentdeck codex-notify  [internal] invoked by Codex's notify hook
//	agentdeck __ui          [internal] the manager UI, run inside tmux
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tallu-wonder/agentdeck/internal/codexnotify"
	"github.com/tallu-wonder/agentdeck/internal/hooks"
	"github.com/tallu-wonder/agentdeck/internal/notify"
	"github.com/tallu-wonder/agentdeck/internal/paths"
	"github.com/tallu-wonder/agentdeck/internal/state"
	"github.com/tallu-wonder/agentdeck/internal/status"
	"github.com/tallu-wonder/agentdeck/internal/tmuxctl"
	"github.com/tallu-wonder/agentdeck/internal/ui"
)

// version is the fallback for builds made outside the module system (a plain
// `go build` of a source tree). Installs via `go install …@version` and release
// builds carry real version metadata, which buildVersion prefers.
const version = "0.1.0"

// buildVersion reports the module version Go recorded at build time, falling
// back to the constant above plus the VCS revision when there is one.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	// A tagged install (`go install …@v1.2.3`) reports that tag. An untagged
	// build reports a v0.0.0- pseudo-version, which is noise — prefer the
	// constant plus the commit in that case.
	if v := info.Main.Version; v != "" && v != "(devel)" && !strings.HasPrefix(v, "v0.0.0-") {
		return v
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 7 {
				rev = s.Value[:7]
			}
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	out := version
	if rev != "" {
		out += "+" + rev
	}
	if dirty {
		out += "-dirty"
	}
	return out
}

// returnKey is the prefix-less tmux binding that jumps back to the manager
// from inside any session. Overridable with AGENTDECK_RETURN_KEY (tmux key
// syntax, e.g. "C-Space").
func returnKey() string {
	if k := os.Getenv("AGENTDECK_RETURN_KEY"); k != "" {
		return k
	}
	return `C-\`
}

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "":
		err = bootstrap()
	case "hook":
		runHook() // never fails outward: must not disturb Claude Code
	case "install-hooks":
		_, err = installHooks(true)
	case "focus":
		arg := ""
		if len(os.Args) > 2 {
			arg = os.Args[2]
		}
		runFocus(arg) // best-effort: invoked by a notification click
	case "codex-notify":
		runCodexNotify(os.Args[2:]) // never fails outward: must not disturb Codex
	case "install-codex-notify":
		err = installCodexNotify(true, hasFlag(os.Args[2:], "--force"))
	case "uninstall-codex-notify":
		err = codexnotify.Uninstall()
	case "__ui":
		err = runUI()
	case "__viewport":
		runViewportPlaceholder()
	case "_tab":
		arg := ""
		if len(os.Args) > 2 {
			arg = os.Args[2]
		}
		runTab(arg) // best-effort; never propagate errors into tmux run-shell
	case "_tabclose":
		arg := ""
		if len(os.Args) > 2 {
			arg = os.Args[2]
		}
		runTabClose(arg)
	case "_tabdrop":
		arg := ""
		if len(os.Args) > 2 {
			arg = os.Args[2]
		}
		runTabDrop(arg)
	case "_tabmenu":
		arg, mx := "", ""
		if len(os.Args) > 2 {
			arg = os.Args[2]
		}
		if len(os.Args) > 3 {
			mx = os.Args[3]
		}
		runTabMenu(arg, mx)
	case "_tabold":
		arg := ""
		if len(os.Args) > 2 {
			arg = os.Args[2]
		}
		runTabOld(arg)
	case "version", "--version", "-v":
		fmt.Println("agentdeck", buildVersion())
	case "help", "--help", "-h":
		usage()
	default:
		usage()
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentdeck:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`agentdeck — a persistent work desk for Claude Code and Codex sessions

usage:
  agentdeck                open the manager (from anywhere; wraps itself in tmux)
  agentdeck install-hooks  (re)install Claude Code status hooks
  agentdeck install-codex-notify
                           chain agentdeck into Codex's notify hook
  agentdeck uninstall-codex-notify
                           restore the notify program agentdeck displaced
  agentdeck focus <id>     show a session (what a notification click runs)
  agentdeck version        print version

environment:
  AGENTDECK_HOME           data dir (default ~/.agentdeck)
  AGENTDECK_CLAUDE_CMD     claude binary to launch (default: "claude" from PATH)
  AGENTDECK_CODEX_CMD      codex binary to launch (default: "codex" from PATH)
  AGENTDECK_RETURN_KEY     tmux key that returns to the manager (default C-\)
  AGENTDECK_CODEX_HOME     where Codex config + sessions live (default ~/.codex)
  AGENTDECK_OPEN_CMD       program that opens folders (default: open / xdg-open)
  AGENTDECK_NOTIFY         desktop alerts: unset (default) | needsyou | all | off
  AGENTDECK_NO_HOOKS       set to any value to never touch Claude's settings.json
  AGENTDECK_PRICING        JSON price table for the cost estimate (see README)
`)
}

// bootstrap gets the user into the manager UI regardless of where they ran
// agentdeck: outside tmux it execs into a tmux session hosting the UI; inside
// tmux it creates the manager session if needed and switches the client.
func bootstrap() error {
	if !tmuxctl.Available() {
		return fmt.Errorf("tmux is required (brew install tmux)")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}

	// The command string is parsed by a shell: quote the binary path.
	uiCmd := shellQuote(self) + " __ui"

	// $TMUX can be stale (inherited from a killed server): only trust it if
	// the server actually answers.
	if tmuxctl.InsideTmux() && tmuxctl.ServerAlive() {
		if !tmuxctl.Has(tmuxctl.ManagerSession) {
			if err := tmuxctl.NewSession(tmuxctl.ManagerSession, mustHome(), nil, uiCmd); err != nil &&
				!tmuxctl.Has(tmuxctl.ManagerSession) { // lost a create race → just switch
				return err
			}
		}
		return tmuxctl.SwitchClient(tmuxctl.ManagerSession)
	}

	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	// Replace this process with tmux attached to the manager session
	// (creating it if needed). Drop any stale $TMUX so tmux doesn't refuse
	// to nest.
	env := os.Environ()
	for i := 0; i < len(env); i++ {
		if strings.HasPrefix(env[i], "TMUX=") {
			env = append(env[:i], env[i+1:]...)
			i--
		}
	}
	argv := []string{"tmux", "new-session", "-A", "-s", tmuxctl.ManagerSession, uiCmd}
	return syscall.Exec(tmuxBin, argv, env)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func mustHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return h
}

// managerLock is held for the manager's lifetime: the flock lives on the open
// descriptor, so the reference must outlive acquireManagerLock.
var managerLock *os.File

// releaseManagerLock drops the single-manager lock. The kernel would release it
// on exit anyway; doing it explicitly means the next manager can start without
// waiting for this process to be reaped.
func releaseManagerLock() {
	if managerLock == nil {
		return
	}
	_ = syscall.Flock(int(managerLock.Fd()), syscall.LOCK_UN)
	_ = managerLock.Close()
	managerLock = nil
}

// acquireManagerLock guarantees a single manager per AGENTDECK_HOME: two
// managers would fight over state.json, last writer winning.
func acquireManagerLock() error {
	f, err := os.OpenFile(filepath.Join(paths.Home(), "manager.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil // lock unavailable: don't block startup over it
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("another agentdeck manager is already running (tmux session %q)", tmuxctl.ManagerSession)
	}
	managerLock = f
	return nil
}

// runUI starts the manager TUI (already inside tmux).
func runUI() error {
	if err := paths.EnsureDirs(); err != nil {
		return err
	}
	if err := acquireManagerLock(); err != nil {
		return err
	}
	// Hook installation is normally automatic, but it edits a file the user
	// owns. AGENTDECK_NO_HOOKS is how someone who removed the entries on
	// purpose keeps them removed, instead of having them reappear every start.
	firstRun := false
	if os.Getenv("AGENTDECK_NO_HOOKS") == "" {
		var err error
		firstRun, err = installHooks(false)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentdeck: warning: could not install Claude hooks:", err)
		}
	}
	// Codex's notify slot holds exactly ONE program, and other tools manage it
	// too, so agentdeck never edits it unattended — two notify keys make Codex
	// refuse to start. Codex status comes from its transcript by default; run
	// `agentdeck install-codex-notify` to chain in for event-driven status.
	self, err := os.Executable()
	if err != nil {
		return err
	}
	recordHost()
	_ = tmuxctl.BindReturnKey(returnKey())
	_ = tmuxctl.BindStatusClicks(shellQuote(self))
	tmuxctl.ConfigureServer()
	tmuxctl.ConfigureManagerSession()

	st, err := state.Load(paths.StateFile())
	if err != nil {
		return err
	}

	claudeBin := os.Getenv("AGENTDECK_CLAUDE_CMD")
	if claudeBin == "" {
		claudeBin, err = exec.LookPath("claude")
		if err != nil {
			claudeBin = "claude" // resolved at wake time by the shell
		}
	}

	defer releaseManagerLock()
	err = superviseUI(st, claudeBin, self, firstRun)
	// Quitting the manager closes the whole agentdeck window (viewport
	// included); the agents keep running detached.
	_ = tmuxctl.KillSession(tmuxctl.ManagerSession)
	return err
}

// crashBurst is how many crashes in crashWindow are treated as a loop rather
// than bad luck: past that, restarting only hides the problem behind a flicker.
const (
	crashBurst  = 3
	crashWindow = time.Minute
)

// superviseUI runs the sidebar and brings it back if it dies unexpectedly.
//
// A panic in the UI would otherwise take the whole desk with it: the pane exits,
// the layout collapses, and what the user sees is a toolbar that vanished for no
// stated reason. The sessions themselves are unaffected — they are separate tmux
// sessions — so the honest recovery is to start the sidebar again, say so, and
// leave a crash report behind. Repeated crashes stop the loop instead of
// flickering forever.
func superviseUI(st *state.State, claudeBin, self string, firstRun bool) error {
	var crashes []time.Time
	note := ""
	for {
		m := ui.New(st, paths.StateFile(), claudeBin, self, firstRun)
		if note != "" {
			m.Notef("%s", note)
			note = ""
		}
		err, panicked := runUIOnce(m)
		if !panicked {
			return err // clean quit, or a real error worth reporting
		}
		firstRun = false

		now := time.Now()
		crashes = append(crashes, now)
		for len(crashes) > 0 && now.Sub(crashes[0]) > crashWindow {
			crashes = crashes[1:]
		}
		if len(crashes) >= crashBurst {
			fmt.Fprintf(os.Stderr,
				"agentdeck: the sidebar crashed %d times in under a minute; giving up.\n"+
					"Your sessions are still running — see %s\n", len(crashes), paths.CrashFile())
			return fmt.Errorf("sidebar crashed repeatedly")
		}
		// Reload from disk: the model that crashed may have left it half-edited.
		if reloaded, lerr := state.Load(paths.StateFile()); lerr == nil {
			st = reloaded
		}
		note = "the sidebar crashed and restarted — see " + paths.CrashFile()
	}
}

// runUIOnce runs the Bubble Tea program, reporting whether it panicked. Bubble
// Tea restores the terminal before re-panicking, so the screen is usable again
// by the time this recovers.
func runUIOnce(m *ui.Model) (err error, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			writeCrashReport(r, debug.Stack())
			panicked = true
		}
	}()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = p.Run()
	return err, false
}

// writeCrashReport appends what happened to the crash file, so a restart is
// explainable after the fact rather than a mystery.
func writeCrashReport(reason any, stack []byte) {
	f, ferr := os.OpenFile(paths.CrashFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "\n=== %s ===\npanic: %v\n%s\n",
		time.Now().Format(time.RFC3339), reason, stack)
}

// runViewportPlaceholder fills the viewport pane while no session is open.
// It must simply exist and never exit on its own.
func runViewportPlaceholder() {
	fmt.Print("\x1b[2J\x1b[H\n\n\n")
	for _, l := range []string{
		"\x1b[1;36m   agentdeck\x1b[0m",
		"",
		"   no session open",
		"",
		"   \x1b[2mpick one on the left —\x1b[0m",
		"   \x1b[2menter opens · n starts new · i imports\x1b[0m",
		"   \x1b[2mC-\\ toggles sidebar ⇄ session\x1b[0m",
	} {
		fmt.Println(l)
	}
	for { // block until the manager respawns this pane
		time.Sleep(time.Hour)
	}
}

// runTabClose handles middle-click on a tab: the session's process is
// killed but its desk entry stays (dormant). The manager notices on its
// next tick and moves the viewport along.
func runTabClose(arg string) {
	arg = strings.TrimPrefix(arg, "user|")
	if arg == "" || arg == "manager" {
		return
	}
	name := state.TmuxName(arg)
	if tmuxctl.Has(name) {
		_ = tmuxctl.KillSession(name)
	}
}

// dragFile remembers which tab a MouseDown grabbed, so a following
// MouseDragEnd can reorder.
func dragFile() string { return filepath.Join(paths.Home(), "tabdrag.json") }

type dragRec struct {
	From string `json:"from"`
	At   int64  `json:"at"`
}

// runTabDrop finishes a tab drag: the tab grabbed on mouse-down is moved
// next to the tab under the mouse on release. The actual state change is
// queued for the manager (single writer of state.json).
func runTabDrop(arg string) {
	arg = strings.TrimPrefix(arg, "user|")
	data, err := os.ReadFile(dragFile())
	if err != nil {
		return
	}
	os.Remove(dragFile())
	var d dragRec
	if json.Unmarshal(data, &d) != nil || d.From == "" {
		return
	}
	if time.Now().UnixMilli()-d.At > 15_000 {
		return // stale grab
	}
	if arg == "" || arg == "manager" || arg == d.From {
		return
	}
	cmd, _ := json.Marshal(map[string]string{"op": "tabdrop", "from": d.From, "to": arg})
	_ = os.MkdirAll(paths.CmdDir(), 0o700)
	_ = os.WriteFile(filepath.Join(paths.CmdDir(), fmt.Sprintf("drop-%d.json", time.Now().UnixNano())), cmd, 0o600)
}

// queueCmd hands a state mutation to the manager (single writer of
// state.json) via the command directory.
// recordHost saves which terminal application the manager is running in.
// Notifications are clicked while another app is frontmost, so `focus` needs to
// know what to raise; only the manager knows, because it is the process that
// was launched from that terminal.
func recordHost() {
	term := tmuxctl.HostTermProgram()
	host := map[string]string{
		"term_program": term,
		"app":          notify.TerminalApp(term),
	}
	data, err := json.Marshal(host)
	if err != nil {
		return
	}
	_ = os.MkdirAll(paths.Home(), 0o700)
	_ = os.WriteFile(paths.HostFile(), data, 0o600)
}

// runFocus shows one session: it raises the terminal running the manager and
// asks the manager to switch its viewport. Invoked by a notification click, so
// it must be silent and fast, and must not care whether the manager is up (the
// queued command is picked up whenever it next runs).
func runFocus(id string) {
	if id == "" {
		return
	}
	st, err := state.Load(paths.StateFile())
	if err != nil || st.Session(id) == nil {
		return // unknown session: nothing to focus
	}
	queueCmd("focus", "", id)

	var host struct{ App string }
	if data, err := os.ReadFile(paths.HostFile()); err == nil {
		_ = json.Unmarshal(data, &host)
	}
	if host.App == "" || runtime.GOOS != "darwin" {
		return
	}
	// The app name comes from our own fixed table above, never from the
	// session or the notification, so it is safe to build a script with.
	_ = exec.Command("osascript", "-e", "tell application \""+host.App+"\" to activate").Run()
}

func queueCmd(op, from, to string) {
	cmd, _ := json.Marshal(map[string]string{"op": op, "from": from, "to": to})
	_ = os.MkdirAll(paths.CmdDir(), 0o755)
	_ = os.WriteFile(filepath.Join(paths.CmdDir(), fmt.Sprintf("%s-%d.json", op, time.Now().UnixNano())), cmd, 0o600)
}

// runCodexNotify handles one Codex notify event: forward it to whatever
// program agentdeck displaced, then record the session's status. Silent and
// non-blocking by design — Codex waits on this process.
func runCodexNotify(args []string) {
	codexnotify.Forward(args)

	ev, ok := codexnotify.ParseEvent(args)
	if !ok {
		return
	}
	st, err := state.Load(paths.StateFile())
	if err != nil {
		return
	}
	// Match the event to a desk entry: by the Codex thread id when we already
	// know it, else by the AGENTDECK_ID of the session we launched (which is
	// how we learn the thread id in the first place).
	var target *state.Session
	if ev.ThreadID != "" {
		for i := range st.Sessions {
			s := &st.Sessions[i]
			if s.AgentOf() == state.AgentCodex && s.SessionID == ev.ThreadID {
				target = s
				break
			}
		}
	}
	if target == nil {
		if id := os.Getenv("AGENTDECK_ID"); id != "" {
			if s := st.Session(id); s != nil && s.AgentOf() == state.AgentCodex {
				target = s
			}
		}
	}
	if target == nil {
		return
	}

	r := status.Read(paths.StatusDir(), target.ID)
	if ev.ThreadID != "" {
		r.ClaudeSessionID = ev.ThreadID // the manager folds this into state
	}
	switch t := strings.ToLower(ev.Type); {
	case strings.Contains(t, "approval"), strings.Contains(t, "permission"),
		strings.Contains(t, "request"):
		r.Status = status.NeedsYou
		r.Message = ev.Type
	case strings.Contains(t, "complete"), strings.Contains(t, "ended"),
		strings.Contains(t, "finish"):
		r.Status = status.Attention
		r.Message = ""
	default:
		return // an event we don't map: leave the current status alone
	}
	_ = status.Write(paths.StatusDir(), target.ID, r)
}

// hasFlag reports whether argv contains a flag.
func hasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

// installCodexNotify chains agentdeck into Codex's notify hook.
func installCodexNotify(verbose, force bool) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	what, err := codexnotify.Install(self, force)
	if err != nil {
		return err
	}
	if verbose {
		fmt.Println("codex notify:", what, "("+paths.CodexConfigFile()+")")
	}
	return nil
}

// runTabOld archives a session from the tab context menu: kill the process
// now, queue the desk change for the manager.
func runTabOld(arg string) {
	arg = strings.TrimPrefix(arg, "user|")
	if arg == "" || arg == "manager" || strings.HasPrefix(arg, "grp:") {
		return
	}
	if name := state.TmuxName(arg); tmuxctl.Has(name) {
		_ = tmuxctl.KillSession(name)
	}
	queueCmd("archive", arg, "")
}

// runTabMenu shows a native tmux popup menu for the right-clicked tab.
// runTabMenu asks the manager to show its context menu for a tab.
//
// tmux's own display-menu is not usable here: a menu opened from a mouse binding
// closes when the button is released, and -O only prevents that when tmux itself
// issues the command — this runs as a separate process, so the release killed it
// every time. Handing the click to the manager gets the desk's own menu, which
// stays up, carries the session's name, and uses the same keys as the sidebar.
func runTabMenu(arg, _ string) {
	arg = strings.TrimPrefix(arg, "user|")
	if arg == "" || arg == "manager" || strings.HasPrefix(arg, "grp:") {
		return
	}
	if !state.ValidID(arg) {
		return
	}
	queueCmd("tabmenu", "", arg)
}

// runTab handles clicks on the tab bar and [ ] cycling, invoked by tmux
// run-shell. Stateless: reads the desk, finds the viewport, switches it.
func runTab(arg string) {
	arg = strings.TrimPrefix(arg, "user|")
	if arg == "" {
		return
	}
	if gid, ok := strings.CutPrefix(arg, "grp:"); ok {
		// clicking a collapsed group's tab expands it
		queueCmd("expand-group", "", gid)
		return
	}
	if arg != "manager" && arg != "next" && arg != "prev" {
		// Remember the grabbed tab in case this click turns into a drag.
		rec, _ := json.Marshal(dragRec{From: arg, At: time.Now().UnixMilli()})
		_ = os.WriteFile(dragFile(), rec, 0o600)
	}
	panes, err := tmuxctl.Panes()
	if err != nil {
		return
	}
	var vpTTY, vpPane, sidebar string
	for _, p := range panes {
		switch p.Role {
		case "viewport":
			vpTTY, vpPane = p.TTY, p.ID
		case "sidebar":
			sidebar = p.ID
		}
	}
	if arg == "manager" {
		if sidebar != "" {
			_ = tmuxctl.SelectPane(sidebar)
		}
		return
	}
	if vpPane == "" {
		return
	}
	st, err := state.Load(paths.StateFile())
	if err != nil {
		return
	}
	live := tmuxctl.ListSessions()
	target := ""
	switch arg {
	case "next", "prev":
		collapsed := map[string]bool{}
		for _, g := range st.Groups {
			if g.Collapsed {
				collapsed[g.ID] = true
			}
		}
		var liveIDs []string
		cur := -1
		active := state.SessionIDFromTmux(tmuxctl.ClientSessions()[vpTTY])
		// sidebar order: ungrouped first, then groups in group order
		addMembers := func(gid string) {
			for i := range st.Sessions {
				s := &st.Sessions[i]
				if s.GroupID != gid || s.Archived || collapsed[gid] {
					continue
				}
				if _, ok := live[state.TmuxName(s.ID)]; !ok {
					continue
				}
				if s.ID == active {
					cur = len(liveIDs)
				}
				liveIDs = append(liveIDs, s.ID)
			}
		}
		addMembers("")
		for _, g := range st.Groups {
			addMembers(g.ID)
		}
		if len(liveIDs) == 0 {
			return
		}
		d := 1
		if arg == "prev" {
			d = -1
		}
		target = liveIDs[((cur+d)%len(liveIDs)+len(liveIDs))%len(liveIDs)]
	default:
		if s := st.Session(arg); s != nil {
			target = s.ID
		}
	}
	if target == "" {
		return
	}
	name := state.TmuxName(target)
	if _, ok := live[name]; !ok {
		return
	}
	if sess, ok := tmuxctl.ClientSessions()[vpTTY]; ok && state.SessionIDFromTmux(sess) != "" {
		_ = tmuxctl.SwitchClientOn(vpTTY, name)
	} else {
		_ = tmuxctl.RespawnPane(vpPane, "TMUX= exec tmux attach-session -t "+shellQuote("="+name))
	}
}

// installHooks ensures agentdeck's hooks are registered in Claude Code
// settings; returns whether anything was added.
func installHooks(verbose bool) (bool, error) {
	self, err := os.Executable()
	if err != nil {
		return false, err
	}
	settings := paths.ClaudeSettingsFile()
	if settings == "" {
		return false, fmt.Errorf("cannot locate Claude settings file")
	}
	changed, err := hooks.Install(settings, self)
	if err != nil {
		return false, err
	}
	if verbose {
		if changed {
			fmt.Println("installed agentdeck hooks into", settings)
		} else {
			fmt.Println("agentdeck hooks already installed in", settings)
		}
	}
	return changed, nil
}

// runHook handles one Claude Code hook event. It must be fast and silent:
// stdout of some hooks is injected into Claude's context, and a non-zero
// exit can block Claude's turn. All failures are swallowed deliberately.
func runHook() {
	id := os.Getenv("AGENTDECK_ID")
	if id == "" || !state.ValidID(id) {
		return // not an agentdeck-managed session, or a tampered id
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return
	}
	var ev status.HookEvent
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	dir := paths.StatusDir()
	prev := status.Read(dir, id)
	if next, ok := status.Apply(prev, ev); ok {
		_ = status.Write(dir, id, next)
	}
}
