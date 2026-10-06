#!/bin/sh
# review-env/up.sh — bring up a REAL draiver webui (drv-020, resolution #51) over a
# throwaway data dir, so a reviewer can click around the UI changes under review without
# touching — or clashing with — the operator's own running draiver.
#
# It IS the UI under review: the webui is built FROM this detached review worktree (the
# reviewed commit), so the instance a reviewer drives reflects exactly the change.
#
# Isolation (the "must not clash with the current running environment" requirement):
#   - binds the draiver-allocated DRAIVER_REVIEW_PORT, never the default 7777;
#   - exports DRAIVER_DATA / DRAIVER_CONFIG → a per-instance .review-env-run/ work area
#     inside this worktree, so every board write lands in dummy data, never in
#     ~/.draiver. Concurrent review envs each own a worktree ⇒ own work area ⇒ no
#     collision; draiver force-removes the worktree on teardown, discarding it all.
#
# ENV_NAME — the web-UI-settable param (resolution #45) draiver injects alongside
# DRAIVER_REVIEW_INSTANCE/PORT — titles the seeded dummy tickets, so the custom value
# visibly parameterises the running instance.
set -e
cd "$(dirname "$0")/.."            # worktree root (go.mod lives here)

RUN=.review-env-run
DATA="$PWD/$RUN/data"
CONFIG="$PWD/$RUN/config.json"
BIN="$PWD/$RUN/draiver"
LOG="$PWD/$RUN/webui.log"
PID="$PWD/$RUN/webui.pid"
ENV_NAME="${ENV_NAME:-review}"

mkdir -p "$DATA"
echo '{}' >"$CONFIG"

# Build the webui from the reviewed commit, so the running instance is the change.
go build -o "$BIN" .

export DRAIVER_DATA="$DATA"
export DRAIVER_CONFIG="$CONFIG"

# Seed a little dummy data across the board's states so there is something to click.
# Best-effort: the webui must still come up over an empty board if a seed hiccups, and
# `escalate` exits non-zero (it signals a block) so it must not trip `set -e`.
"$BIN" new    DUMMY-1 --title "$ENV_NAME: running ticket" --repo "$PWD" --base main >/dev/null 2>&1 || true
"$BIN" new    DUMMY-2 --title "$ENV_NAME: in review"      --repo "$PWD" --base main >/dev/null 2>&1 || true
"$BIN" review DUMMY-2 "seeded review claim for $ENV_NAME" --url "http://example.test/compare/main...dummy" >/dev/null 2>&1 || true
"$BIN" new    DUMMY-3 --title "$ENV_NAME: needs a human"  --repo "$PWD" --base main >/dev/null 2>&1 || true
"$BIN" escalate DUMMY-3 "seeded question for $ENV_NAME?" >/dev/null 2>&1 || true

# Launch the webui backgrounded with output redirected (so draiver's CombinedOutput does
# not block on the inherited pipe), record its pid for down.sh, then wait until the port
# actually answers so confirm-up never races bring-up.
"$BIN" webui --addr "127.0.0.1:$DRAIVER_REVIEW_PORT" >"$LOG" 2>&1 &
echo $! >"$PID"

i=0
while [ "$i" -lt 150 ]; do
  if node -e "require('net').connect(Number(process.env.DRAIVER_REVIEW_PORT),'127.0.0.1').on('connect',function(){process.exit(0)}).on('error',function(){process.exit(1)})" 2>/dev/null; then
    echo "up: draiver webui listening on $DRAIVER_REVIEW_PORT (ENV_NAME=$ENV_NAME, data=$DATA)"
    exit 0
  fi
  i=$((i + 1))
  sleep 0.1
done

echo "up: webui never listened on $DRAIVER_REVIEW_PORT" >&2
cat "$LOG" >&2 || true
exit 1
