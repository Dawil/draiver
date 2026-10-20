#!/bin/sh
# review-env/ttyd/down.sh — tear down the ttyd CLI review env (drv-032 #12, drv-020 #22).
# draiver replays the same DRAIVER_REVIEW_INSTANCE it injected into up.sh, so we target the
# per-instance container by name; force-removing it releases the published DRAIVER_REVIEW_PORT
# that draiver's confirm-down healthcheck then probes (now-red = confirmed down). The image
# and the worktree's .build/ are discarded when draiver force-removes the review worktree
# right after this returns, so nothing leaks. Best-effort by design — draiver's confirm-down
# probe, not this exit code, is the authority on whether the env actually went away.
set -eu
INSTANCE="${DRAIVER_REVIEW_INSTANCE:-ttyd-review}"
IMAGE="draiver-ttyd-review:${INSTANCE}"
NAME="draiver-ttyd-review-${INSTANCE}"

docker rm -f "$NAME"   >/dev/null 2>&1 && echo "down: removed container $NAME" || echo "down: $NAME not running"
docker rmi    "$IMAGE" >/dev/null 2>&1 || true
exit 0
