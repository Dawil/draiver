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
# None records cut provenance, so base_commit shows as `inferred` / `unknown`.
#
# Then try:
#   draiver metrics --codebase WORKDIR/widget-a                # stored: all missing
#   draiver metrics --codebase WORKDIR/widget-b --compute      # same codebase, computed
#   draiver metrics --codebase WORKDIR/widget-a --sort exploration_prefix_calls --limit 2
#   draiver metrics WIDGET-1 --json
set -eu

: "${DRAIVER_DATA:?DRAIVER_DATA must be set}"
BIN="${DRAIVER_BIN:-draiver}"
WORK="${1:?usage: seed-metrics.sh WORKDIR}"
export DRAIVER_ACTOR="${DRAIVER_ACTOR:-agent:seed}"
FIXTURE="$(cd "$(dirname "$0")/.." && pwd)/features/metrics/fixtures/widget.stream.jsonl"

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
