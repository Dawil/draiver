#!/bin/sh
# review-env/seed-metrics.sh — seed attempts with recorded transcripts so a reviewer can
# run `draiver metrics` against real output (drv-054).
#
# Usage: DRAIVER_DATA=... DRAIVER_BIN=... sh review-env/seed-metrics.sh WORKDIR
#
# Stands up, under WORKDIR, one bare remote and two local clones of it (widget-a,
# widget-b) — two paths, one codebase — and seeds three attempts whose session
# transcripts are the drv-054 widget fixture (features/metrics/fixtures/):
#   WIDGET-1  widget-a, branch with a one-file diff, in review,  model claude-fixture-1
#   WIDGET-2  widget-b, branch with a one-file diff, ratified,   model claude-fixture-2
#             (a lighter transcript: three exploration calls dropped)
#   WIDGET-3  widget-a, no branch (diff unavailable), escalated, model claude-fixture-1
#   WIDGET-40, WIDGET-41  widget-a, the metrics-v2 sessions fixture (two sessions, three
#             segments, a compaction, ranged and cross-session rereads, a read of
#             src/steps_widget12.go, a stale-binary `unknown command`, a transient
#             tmp/notes.txt, and a shipped zz_widgetNN_focus_test.go), with a
#             context-auto-stop escalation and a review on the log
#   (drv-057) widget-a's main then gains C1, an AGENTS.md edit tagged agents-md-change:
#   WIDGET-50, WIDGET-51  widget-a, cut after C1, the lighter widget transcript plus a
#             scratch `cd /tmp/widget-scratch && cat > a.md` that metrics v3 ignores
#   so `--split-at agents-md-change` puts WIDGET-1/40/41 before, WIDGET-50/51 after, and
#   WIDGET-3 (no branch, base unknown) in the excluded count.
# None records cut provenance, so base_commit shows as `inferred` / `unknown`. The seeded
# log is written at seed time, after the fixture's whole clock, so every log ending
# lands in the LAST session's (open-ended) window: session 1 reads `ended_by none` and
# session 2 reads `context-auto-stop`. The @drv-054 BDD world logs on the fixture's
# clock and gets the real split (context-auto-stop, then review).
#
# Then try:
#   draiver metrics --codebase WORKDIR/widget-a                # stored: all missing
#   draiver metrics --codebase WORKDIR/widget-b --compute      # same codebase, computed
#   draiver metrics --codebase WORKDIR/widget-a --sort exploration_prefix_calls --limit 2
#   draiver metrics WIDGET-1 --json
#   draiver metrics WIDGET-40 --compute                        # sessions, invention
#   draiver metrics --codebase WORKDIR/widget-a --compute --by file
#   draiver metrics --codebase WORKDIR/widget-a --compute --by command
#   draiver metrics --codebase WORKDIR/widget-a --compute --split-at agents-md-change
#   draiver metrics --codebase WORKDIR/widget-a --compute --split-at agents-md-change --by file
set -eu

: "${DRAIVER_DATA:?DRAIVER_DATA must be set}"
BIN="${DRAIVER_BIN:-draiver}"
WORK="${1:?usage: seed-metrics.sh WORKDIR}"
export DRAIVER_ACTOR="${DRAIVER_ACTOR:-agent:seed}"
FIXTURES="$(cd "$(dirname "$0")/.." && pwd)/features/metrics/fixtures"
FIXTURE="$FIXTURES/widget.stream.jsonl"

rm -rf "$WORK"
mkdir -p "$WORK"
WORK="$(cd "$WORK" && pwd -P)"
g() { git -c user.email=seed@review.local -c user.name="review seed" "$@"; }

g init -q --bare -b main "$WORK/origin.git"
g clone -q "$WORK/origin.git" "$WORK/widget-a" 2>/dev/null
A="$WORK/widget-a"
g -C "$A" checkout -q -b main
mkdir -p "$A/src" "$A/docs"
printf '# Agents\nRun `go test ./...`.\n' >"$A/AGENTS.md"
printf '# widget\n' >"$A/README.md"
printf '# Guide\n' >"$A/docs/guide.md"
printf 'package widget\n\nfunc widget() int {\n\treturn 1\n}\n' >"$A/src/widget.go"
printf 'package widget // unused\n' >"$A/src/unused.go"
i=1; while [ $i -le 200 ]; do printf 'line %03d of big.go\n' $i; i=$((i+1)); done >"$A/src/big.go"
printf 'package widget // WIDGET-12 steps\n' >"$A/src/steps_widget12.go"
g -C "$A" add -A
g -C "$A" commit -qm widget
g -C "$A" push -q origin main
g clone -q "$WORK/origin.git" "$WORK/widget-b"
B="$WORK/widget-b"

# fix_on BRANCH REPO: commit the widget fix on BRANCH, leaving REPO on main.
fix_on() {
  g -C "$2" checkout -q -b "$1"
  printf 'package widget\n\nfunc widget() int {\n\treturn 2\n}\n' >"$2/src/widget.go"
  g -C "$2" commit -qam "Fix widget"
  g -C "$2" checkout -q main
}

# transcript TICKET FILE: install FILE as the attempt's recorded session stream.
transcript() {
  mkdir -p "$DRAIVER_DATA/$1/attempts/0001/session"
  cp "$2" "$DRAIVER_DATA/$1/attempts/0001/session/stream.jsonl"
}

"$BIN" new WIDGET-1 --title "metrics: widget fix, in review" --repo "$A" --base main >/dev/null
transcript WIDGET-1 "$FIXTURE"
fix_on draiver/WIDGET-1/0001 "$A"
"$BIN" review WIDGET-1 "widget fixed" --url "http://example.test/compare/main...draiver/WIDGET-1/0001" >/dev/null

"$BIN" new WIDGET-2 --title "metrics: widget fix in a second clone, ratified" --repo "$B" --base main >/dev/null
grep -v -e '"toolu_fixture_04"' -e '"toolu_fixture_05"' -e '"toolu_fixture_06"' "$FIXTURE" \
  | sed 's/claude-fixture-1/claude-fixture-2/g' >"$WORK/lighter.stream.jsonl"
transcript WIDGET-2 "$WORK/lighter.stream.jsonl"
fix_on draiver/WIDGET-2/0001 "$B"
"$BIN" review WIDGET-2 "widget fixed" --url "http://example.test/compare/main...draiver/WIDGET-2/0001" >/dev/null
DRAIVER_ACTOR=human:review "$BIN" done WIDGET-2 >/dev/null

"$BIN" new WIDGET-3 --title "metrics: widget fix, escalated" --repo "$A" --base main >/dev/null
transcript WIDGET-3 "$FIXTURE"
"$BIN" escalate WIDGET-3 "Which widget registry should this use?" >/dev/null 2>&1 || true

# WIDGET-40/41: the metrics-v2 sessions fixture, renamed to the widget tickets.
for n in 40 41; do
  "$BIN" new WIDGET-$n --title "metrics v2: restart-spanning attempt $n" --repo "$A" --base main >/dev/null
  sed -e "s/drv905/widget$n/g" -e "s/drv-905/WIDGET-$n/g" -e "s/drv901/widget12/g" \
    "$FIXTURES/sessions.stream.jsonl" >"$WORK/sessions-$n.stream.jsonl"
  transcript WIDGET-$n "$WORK/sessions-$n.stream.jsonl"
  g -C "$A" checkout -q -b draiver/WIDGET-$n/0001
  printf 'line one of big.go\n' >"$A/src/big.go"
  printf 'package widget\n' >"$A/src/zz_widget${n}_focus_test.go"
  g -C "$A" add -A
  g -C "$A" commit -qm "WIDGET-$n work"
  g -C "$A" checkout -q main
  seq=$("$BIN" log WIDGET-$n --type escalation --tag context-auto-stop "Context window exhausted." | sed -n 's/.*escalation #\([0-9]*\).*/\1/p')
  DRAIVER_ACTOR=human:review "$BIN" resolve WIDGET-$n "$seq" "Restart context window." --attempt 0001 >/dev/null
  "$BIN" review WIDGET-$n "big.go fixed" --url "http://example.test/compare/main...draiver/WIDGET-$n/0001" >/dev/null
done

# drv-057: C1, an AGENTS.md change on main, then two attempts cut after it. Their
# transcript is the lighter one plus a scratch write in /tmp after a `cd`, which
# metrics v3 resolves outside the worktree and drops.
printf '# Agents\nRun `go test ./...`.\nScratch goes in /tmp, never the repo root.\n' >"$A/AGENTS.md"
g -C "$A" commit -qam "AGENTS.md: scratch in /tmp"
g -C "$A" tag agents-md-change
{
  grep -v -e '"type":"result"' "$WORK/lighter.stream.jsonl"
  printf '%s\n' '{"type":"assistant","parent_tool_use_id":null,"session_id":"fixture-session-widget","timestamp":"2026-11-01T10:10:40.000Z","message":{"model":"claude-fixture-2","id":"msg_scratch","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_scratch","name":"Bash","input":{"command":"cd /tmp/widget-scratch && cat > a.md <<EOF\nnotes\nEOF"}}],"usage":{"input_tokens":1000,"cache_read_input_tokens":20000,"cache_creation_input_tokens":0,"output_tokens":100}}}'
  printf '%s\n' '{"type":"user","parent_tool_use_id":null,"session_id":"fixture-session-widget","timestamp":"2026-11-01T10:10:45.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_scratch","content":"","is_error":false}]}}'
} >"$WORK/after.stream.jsonl"
for n in 50 51; do
  "$BIN" new WIDGET-$n --title "metrics: widget fix after the AGENTS.md change ($n)" --repo "$A" --base main >/dev/null
  transcript WIDGET-$n "$WORK/after.stream.jsonl"
  fix_on draiver/WIDGET-$n/0001 "$A"
  "$BIN" review WIDGET-$n "widget fixed" --url "http://example.test/compare/main...draiver/WIDGET-$n/0001" >/dev/null
done
