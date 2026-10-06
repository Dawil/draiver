#!/bin/sh
# review-env/up.sh — bring draiver's own review environment up (drv-020). Start the
# server in the background with its output redirected to a file (so draiver's
# CombinedOutput does not block on the inherited pipe), then wait until the injected
# port actually accepts a connection before returning, so confirm-up never races
# bring-up. The server greets with ENV_NAME — the web-UI-settable custom var
# (resolution #45) draiver injects alongside DRAIVER_REVIEW_INSTANCE/PORT.
set -e
cd "$(dirname "$0")"

node server.js >server.log 2>&1 &

i=0
while [ "$i" -lt 100 ]; do
  if node -e "require('net').connect(Number(process.env.DRAIVER_REVIEW_PORT),'127.0.0.1').on('connect',function(){process.exit(0)}).on('error',function(){process.exit(1)})" 2>/dev/null; then
    echo "up: listening on $DRAIVER_REVIEW_PORT (ENV_NAME=$ENV_NAME)"
    exit 0
  fi
  i=$((i + 1))
  sleep 0.1
done

echo "up: server never listened on $DRAIVER_REVIEW_PORT" >&2
cat server.log >&2 || true
exit 1
