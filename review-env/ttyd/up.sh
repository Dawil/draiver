#!/bin/sh
# review-env/ttyd/up.sh — build and run the ttyd web-terminal review env (drv-032 #12) so
# a reviewer can drive the recall CLI changes from a browser terminal over seeded data.
#
# Host-builds the reviewed draiver binary and seeds a board into .build/ (so the image
# needs no toolchain), builds the image, then runs it SANDBOXED:
#   - non-root `reviewer` user (set in the image)
#   - --cap-drop ALL + --security-opt no-new-privileges   (no privileges to escalate)
#   - --pids-limit / --memory                             (no fork/alloc runaway)
#   - published to 127.0.0.1 only                         (not reachable off this host)
# The seeded board is deliberately left writable inside the container so the reviewer can
# also exercise `log`/`--supersedes`; the container is ephemeral (down.sh removes it).
set -eu
cd "$(dirname "$0")/../.."          # worktree root — Docker build context + go.mod

IMAGE=draiver-ttyd-review
NAME=draiver-ttyd-review
PORT="${DRAIVER_TTYD_PORT:-7681}"
BUILD=review-env/ttyd/.build

command -v docker >/dev/null 2>&1 || { echo "up: docker not found on PATH" >&2; exit 1; }
command -v go     >/dev/null 2>&1 || { echo "up: go not found on PATH" >&2; exit 1; }

# 1. Build the reviewed binary (static, so it runs on debian-slim) and seed the board.
rm -rf "$BUILD"
mkdir -p "$BUILD"
echo "up: building draiver (static) ..."
CGO_ENABLED=0 go build -o "$BUILD/draiver" .
echo "up: seeding demo board ..."
DRAIVER_DATA="$PWD/$BUILD/data" DRAIVER_BIN="$PWD/$BUILD/draiver" sh review-env/ttyd/seed.sh

# 2. Build the image.
echo "up: building image $IMAGE ..."
docker build -f review-env/ttyd/Dockerfile -t "$IMAGE" .

# 3. Run it, sandboxed, bound to loopback.
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" \
  -p "127.0.0.1:${PORT}:7681" \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --pids-limit 256 \
  --memory 512m \
  "$IMAGE" >/dev/null

# 4. Wait until ttyd actually answers, so the URL we print is live.
i=0
while [ "$i" -lt 100 ]; do
  if curl -fsS -o /dev/null "http://127.0.0.1:${PORT}/" 2>/dev/null; then
    echo "up: ttyd review terminal live at  http://127.0.0.1:${PORT}/"
    echo "    seeded ticket DEMO-033 (attempts 0001-0004); stop with review-env/ttyd/down.sh"
    exit 0
  fi
  i=$((i + 1))
  sleep 0.1
done

echo "up: ttyd never answered on ${PORT}; container logs:" >&2
docker logs "$NAME" >&2 || true
exit 1
