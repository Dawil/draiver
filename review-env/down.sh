#!/bin/sh
# review-env/down.sh — tear draiver's own review environment down (drv-020): stop the
# server by its pid file, then wait until the injected port is released so confirm-down
# sees it gone. Best-effort by design — draiver's confirm-down healthcheck, not this
# script's exit code, is the authority on whether the env actually went away.
cd "$(dirname "$0")"

if [ -f server.pid ]; then
  kill "$(cat server.pid)" 2>/dev/null || true
fi

i=0
while [ "$i" -lt 100 ]; do
  if node -e "require('net').connect(Number(process.env.DRAIVER_REVIEW_PORT),'127.0.0.1').on('connect',function(){process.exit(0)}).on('error',function(){process.exit(1)})" 2>/dev/null; then
    i=$((i + 1))
    sleep 0.1
  else
    rm -f server.pid
    echo "down: port $DRAIVER_REVIEW_PORT released"
    exit 0
  fi
done

echo "down: port still answering after kill" >&2
exit 0
