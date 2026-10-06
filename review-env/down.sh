#!/bin/sh
# review-env/down.sh — stop the review webui (drv-020, resolution #51) by its pid file,
# then wait until the injected port is released so draiver's confirm-down healthcheck sees
# it gone. Best-effort by design — draiver's confirm-down probe, not this exit code, is the
# authority on whether the env actually went away. The .review-env-run/ work area (dummy
# data + built binary) is discarded when draiver force-removes the worktree right after
# this returns, so nothing leaks.
cd "$(dirname "$0")/.."            # worktree root
RUN=.review-env-run
PID="$PWD/$RUN/webui.pid"

if [ -f "$PID" ]; then
  kill "$(cat "$PID")" 2>/dev/null || true
fi

i=0
while [ "$i" -lt 100 ]; do
  if node -e "require('net').connect(Number(process.env.DRAIVER_REVIEW_PORT),'127.0.0.1').on('connect',function(){process.exit(0)}).on('error',function(){process.exit(1)})" 2>/dev/null; then
    i=$((i + 1))
    sleep 0.1
  else
    rm -f "$PID"
    echo "down: port $DRAIVER_REVIEW_PORT released"
    exit 0
  fi
done

echo "down: port still answering after kill" >&2
exit 0
