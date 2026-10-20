#!/bin/sh
# review-env/ttyd/seed.sh — bake "suitable data" for the recall review env (drv-032 #12).
#
# Produces one multi-attempt ticket (DEMO-033, attempts 0001-0004) whose facts carry a
# varied, reusable tag set plus a supersession, so a reviewer at the ttyd terminal can
# exercise every recall change under review:
#   - `recall DEMO-033 --tags`      distinct tags, alphabetical, with counts
#   - `recall DEMO-033@0001`        the ticket@attempt address form, any mode
#   - `recall DEMO-033`             the attempt-aware header ("latest of 4 attempts")
#   - `recall DEMO-033 --all`       superseded facts surface
#   - `recall DEMO-033 --chain N`   a seemed-X -> now-Y history
#
# Writes only into $DRAIVER_DATA. Run on the host by up.sh, not inside the image, so the
# image needs neither go nor git. Best-effort is NOT acceptable here: a half-seeded board
# makes a confusing demo, so this runs under `set -e` and fails loudly.
set -eu

: "${DRAIVER_DATA:?DRAIVER_DATA must be set}"
BIN="${DRAIVER_BIN:-draiver}"
export DRAIVER_ACTOR="${DRAIVER_ACTOR:-agent:seed}"
TICKET=DEMO-033

# `new`/`attempt new` record repo+base per attempt; base resolution needs a real branch,
# so stand up a throwaway git repo (recall never reads it — only the log matters).
REPO="$(mktemp -d)"
git -C "$REPO" init -q -b main
git -C "$REPO" config user.email seed@review.local
git -C "$REPO" config user.name "review seed"
: >"$REPO/README.md"
git -C "$REPO" add -A
git -C "$REPO" commit -q -m seed

# log <type> <body> [--tag X ...]  — appends to the attempt pinned by $DRAIVER_ATTEMPT
# and echoes the new seq (parsed from "logged <type> #<seq> on ...").
seq_of() { "$BIN" "$@" | grep -oE '#[0-9]+' | head -1 | tr -d '#'; }

"$BIN" new "$TICKET" --title "recall demo: multi-attempt, tagged facts" --repo "$REPO" --base main >/dev/null

# --- attempt 0001 (created by `new`) ---
export DRAIVER_ATTEMPT=0001
"$BIN" log "$TICKET" --type note     --tag plan                   "Attempt 0001 plan: scope the three recall changes (--tags, @attempt, attempt-aware header)." >/dev/null
"$BIN" log "$TICKET" --type decision --tag plan --tag approach    "Reuse resolveCtlTarget for @attempt parsing rather than growing a second split convention." >/dev/null
"$BIN" log "$TICKET" --type gotcha   --tag shell-escape           "Heredoc log bodies need a single-quoted delimiter or the shell eats backticks and dollar signs." >/dev/null

# --- attempt 0002 ---
export DRAIVER_ATTEMPT=0002
"$BIN" attempt new "$TICKET" --from 0001 >/dev/null
"$BIN" log "$TICKET" --type note   --tag env-reset                "Reset DRAIVER_DATA between runs so stale facts never leak into the demo." >/dev/null
"$BIN" log "$TICKET" --type gotcha --tag env-reset --tag shell-escape "A leaked DRAIVER_ATTEMPT from a prior run targeted the wrong attempt — always pin it." >/dev/null

# --- attempt 0003 ---
export DRAIVER_ATTEMPT=0003
"$BIN" attempt new "$TICKET" --from 0001 >/dev/null
"$BIN" log "$TICKET" --type decision --tag plan                   "Chose per-tag counts over names-only for recall --tags — cheap and genuinely useful." >/dev/null
"$BIN" log "$TICKET" --type note     --tag shell-escape           "ttyd serves bash; mind quoting when pasting multi-line commands into the terminal." >/dev/null

# --- attempt 0004 (latest — the one a bare `recall DEMO-033` resolves to) ---
export DRAIVER_ATTEMPT=0004
"$BIN" attempt new "$TICKET" --from 0001 >/dev/null
"$BIN" log "$TICKET" --type note --tag plan --tag env-reset       "Attempt 0004 final pass: the header must name the resolved attempt and flag its siblings." >/dev/null
# A supersession, so --chain and --all have a history to show.
STALE=$(seq_of log "$TICKET" --type decision --tag env-reset      "Tentative: wipe the whole data root on every boot.")
"$BIN" log "$TICKET" --type decision --tag env-reset --supersedes "$STALE" "Reverted: bake the data at build time — a fresh boot keeps the seeded facts instead of an empty board." >/dev/null
"$BIN" log "$TICKET" --type gotcha --tag shell-escape             "Backticks render as code on the board but must still be escaped in an unquoted shell." >/dev/null

echo "seed: DEMO-033 ready (attempts 0001-0004) under $DRAIVER_DATA"
