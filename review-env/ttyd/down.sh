#!/bin/sh
# review-env/ttyd/down.sh — stop and remove the ttyd review env (drv-032 #12). The
# container is ephemeral (seeded board lives only inside it), so a force-rm is the whole
# teardown; .build/ on the host is left for a fast re-`up` and is gitignored.
set -eu
NAME=draiver-ttyd-review
docker rm -f "$NAME" >/dev/null 2>&1 && echo "down: removed $NAME" || echo "down: $NAME not running"
