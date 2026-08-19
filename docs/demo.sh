#!/usr/bin/env bash
# Rebuilds docs/desk.svg, docs/info.svg and docs/keys.svg from a LIVE demo
# desk: a sandboxed agentboss (private tmux server, stub agents, fabricated
# transcripts) driven exactly like the real thing, captured with
# `tmux capture-pane -e` and rendered by ansi2svg.py. Because the screenshots
# come from the running UI, they cannot drift from what the UI renders.
#
#   ./docs/demo.sh
#
# Needs: go, tmux, python3. Touches nothing outside its /tmp sandbox.
set -euo pipefail
cd "$(dirname "$0")/.."

D=$(mktemp -d /tmp/agentboss-demo-XXXXXX)
cleanup() { TMUX_TMPDIR="$D" tmux kill-server 2>/dev/null || true; rm -rf "$D"; }
trap cleanup EXIT

mkdir -p "$D/home" "$D/bin" "$D/projects/demo"
go build -o "$D/agentboss" .

# The stub agent paints a plausible mid-task screen, then holds the pane open.
# Lines stay under 48 cells so they never wrap in the demo viewport.
cat > "$D/bin/fakeagent" <<'AGENT'
#!/bin/bash
printf '\n'
printf ' \033[38;5;252m● Reading \033[1mpayments/charge.ts\033[0m\n'
printf '   \033[38;5;242m⎿  Read 214 lines · 3 call sites\033[0m\n'
printf '\n'
printf ' \033[38;5;252m● The bug: \033[38;5;221mretry()\033[38;5;252m drops the\n'
printf '   idempotency key on its second attempt,\n'
printf '   so a gateway timeout can charge twice.\n'
printf '   Two ways out:\033[0m\n'
printf '\n'
printf '   \033[38;5;252m1. Thread it through \033[38;5;117mRetryContext\033[0m\n'
printf '   \033[38;5;252m2. Pin it in the closure\033[0m\n'
printf '\n'
printf ' \033[38;5;252m● Going with 1 — the closure version\n'
printf '   breaks on concurrent retries anyway.\033[0m\n'
printf '\n'
printf ' \033[38;5;242m✻ Editing charge.ts…\033[0m\n'
exec cat
AGENT
printf '#!/bin/bash\nexit 0\n' > "$D/bin/terminal-notifier"
printf '#!/bin/bash\nexit 0\n' > "$D/bin/open"
chmod +x "$D/bin/"*

SESSIONS=("payments refactor" "flaky login test" "release notes" "PR sweep" "dep bumps")
for n in "${SESSIONS[@]}"; do mkdir -p "$D/work/$n"; done

env -i PATH="$D/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin" \
  TMUX_TMPDIR="$D" TERM=xterm-256color TERM_PROGRAM=ghostty HOME="$D" \
  AGENTBOSS_HOME="$D/home" AGENTBOSS_CLAUDE_SETTINGS="$D/settings.json" \
  AGENTBOSS_CLAUDE_CMD="$D/bin/fakeagent" AGENTBOSS_CLAUDE_PROJECTS="$D/projects" \
  AGENTBOSS_OPEN_CMD="$D/bin/open" \
  tmux new-session -d -s agentboss -x 99 -y 36 "$D/agentboss __ui"
sleep 2

T() { TMUX_TMPDIR="$D" tmux "$@"; }
keys() { local k; for k in "$@"; do T send-keys -t agentboss:0.0 "$k"; sleep 0.18; done; }
lit() { T send-keys -t agentboss:0.0 -l -- "$1"; sleep 0.18; }
# find selects a session row by (unique part of) its name via search. Escape
# (not Enter — that would OPEN the match) leaves search with the selection on
# the matched row and the filter cleared.
find_row() { keys /; lit "$1"; sleep 0.3; keys Escape; sleep 0.2; }

for n in "${SESSIONS[@]}"; do
  keys n C-u; lit "$D/work/$n"; keys Enter Enter; sleep 0.6
done

keys N; lit "shipping"; keys Enter
keys N; lit "reviews";  keys Enter
find_row "payments"; keys m Down Enter       # → shipping
find_row "flaky";    keys m Down Enter       # → shipping
find_row "release";  keys m Down Down Enter  # → reviews
find_row "PR";       keys m Down Down Enter  # → reviews
find_row "dep";      keys x y                # → old shelf
sleep 0.5

# name → desk id, then conversation ids + fabricated transcripts (model,
# context and cost all flow from these).
hookev() {
  env AGENTBOSS_HOME="$D/home" AGENTBOSS_ID="$1" TMUX_TMPDIR="$D" \
    "$D/agentboss" hook <<< "$2"
}
id_of() {
  python3 - "$D" "$1" <<'PY'
import json, sys
st = json.load(open(sys.argv[1] + "/home/state.json"))
print(next(s["id"] for s in st["sessions"] if s["name"] == sys.argv[2]))
PY
}
uuid() { printf '11111111-aaaa-4bbb-8ccc-%012d' "$1"; }
i=1
for n in "${SESSIONS[@]}"; do
  hookev "$(id_of "$n")" "{\"hook_event_name\":\"SessionStart\",\"session_id\":\"$(uuid $i)\"}"
  i=$((i+1))
done
python3 - "$D" <<'PY'
import json, os, sys, time
root = sys.argv[1] + "/projects/demo"
def write(n, model, ctx, turns, out_tok, hours):
    t0 = time.time() - hours * 3600
    uid = "11111111-aaaa-4bbb-8ccc-%012d" % n
    with open(os.path.join(root, uid + ".jsonl"), "w") as f:
        f.write(json.dumps({"type": "user", "cwd": "/tmp",
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(t0))}) + "\n")
        for i in range(turns):
            f.write(json.dumps({"type": "assistant",
                "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(t0 + i * 120)),
                "message": {"role": "assistant", "model": model,
                    "usage": {"input_tokens": 1800, "cache_read_input_tokens": ctx - 3000,
                              "cache_creation_input_tokens": 600, "output_tokens": out_tok}}}) + "\n")
write(1, "claude-opus-4-8-20260115",   185_000, 46, 1500, 5)   # payments refactor
write(2, "claude-opus-4-8-20260115",    96_000, 18,  900, 2)   # flaky login test
write(3, "claude-sonnet-4-5-20250929",  22_000, 12,  700, 1)   # release notes
write(4, "claude-sonnet-4-5-20250929",  54_000, 20,  800, 8)   # PR sweep
write(5, "claude-haiku-4-5-20251001",   11_000,  6,  300, 30)  # dep bumps
PY

hookev "$(id_of 'flaky login test')" '{"hook_event_name":"Notification","message":"Claude needs your permission to use Bash"}'
hookev "$(id_of 'release notes')"    '{"hook_event_name":"Stop"}'
sleep 4

# Keys + info shots at full height, whole window (tab bar included) via a
# nested holder client, with the sidebar holding focus.
SB=$(T list-panes -t agentboss: -F '#{pane_id} #{@agentboss_role}' | awk '$2=="sidebar"{print $1}')
T select-pane -t "$SB"
T new-session -d -s holder -x 99 -y 37 "TMUX= tmux attach-session -t agentboss"
sleep 2
shot() {
  T capture-pane -p -e -J -t holder > "$D/$1.ansi"
  python3 docs/ansi2svg.py "$D/$1.ansi" "docs/$1.svg" agentboss
}
keys '?'; sleep 0.6; shot keys; keys Escape; sleep 0.3
find_row "payments"; keys v; sleep 0.7; shot info; keys Escape; sleep 0.3

# The hero is shorter. Resize first, then reopen the featured session so its
# agent paints at the final size (a repaint after shrinking would otherwise
# leave the top of the canned screen scrolled away).
T kill-session -t holder
T resize-window -t agentboss: -x 99 -y 24
sleep 1
find_row "payments"; keys z y; sleep 1     # close its tab...
find_row "payments"; keys Enter; sleep 2   # ...and reopen at the final size
hookev "$(id_of 'payments refactor')" '{"hook_event_name":"PreToolUse","tool_name":"Edit"}'
T select-pane -t "$SB"
T new-session -d -s holder -x 99 -y 25 "TMUX= tmux attach-session -t agentboss"
sleep 2.5
shot desk

echo "wrote docs/desk.svg docs/info.svg docs/keys.svg"
