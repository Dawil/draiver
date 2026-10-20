#!/bin/sh
# review-env/ttyd/up.sh — bring up the ttyd web-terminal CLI review env (drv-032 #12) as a
# drv-020 reviewEnvironment (#22): a browser terminal over the reviewed draiver binary and a
# pre-seeded board, so a reviewer can drive the recall CLI changes without installing
# anything locally. Declared as the `review-cli` environment in .test-pyramid.yaml and
# launched on demand from an attempt page in Review — NOT by any test level.
#
# draiver injects the parameterisation contract (drv-020): DRAIVER_REVIEW_PORT (an allocated
# free host port to bind) and DRAIVER_REVIEW_INSTANCE (a unique slug). We namespace the
# container/image by the instance and publish the injected port, so concurrent review envs on
# sibling attempts never collide. Run manually (outside draiver) with neither set and it
# falls back to a DRAIVER_TTYD_PORT (default 7681) and a fixed instance name.
#
# #12's "sandboxed docker container" still holds — only the wiring changed (#22): the image
# host-builds the reviewed binary + seeds a board into .build/ (so the image needs no
# toolchain), then runs it sandboxed (non-root, --cap-drop ALL, no-new-privileges, pid/mem
# caps) bound to loopback only. The seeded board is left writable so the reviewer can also
# exercise `log`/`--supersedes`; the container is ephemeral (down.sh removes it).
set -eu
cd "$(dirname "$0")/../.."          # worktree root — Docker build context + go.mod

INSTANCE="${DRAIVER_REVIEW_INSTANCE:-ttyd-review}"
PORT="${DRAIVER_REVIEW_PORT:-${DRAIVER_TTYD_PORT:-7681}}"
IMAGE="draiver-ttyd-review:${INSTANCE}"
NAME="draiver-ttyd-review-${INSTANCE}"
BUILD=review-env/ttyd/.build

command -v docker >/dev/null 2>&1 || { echo "up: docker not found on PATH" >&2; exit 1; }
command -v go     >/dev/null 2>&1 || { echo "up: go not found on PATH" >&2; exit 1; }

# 1. Build the reviewed binary (static, so it runs on debian-slim) and seed the board.
#    Each review env owns its own worktree, so .build/ here is already per-instance.
rm -rf "$BUILD"
mkdir -p "$BUILD"
echo "up: building draiver (static) ..."
CGO_ENABLED=0 go build -o "$BUILD/draiver" .
echo "up: seeding demo board ..."
DRAIVER_DATA="$PWD/$BUILD/data" DRAIVER_BIN="$PWD/$BUILD/draiver" sh review-env/ttyd/seed.sh

# 2. Build the image (tagged per-instance so concurrent builds never clobber each other).
echo "up: building image $IMAGE ..."
docker build -f review-env/ttyd/Dockerfile -t "$IMAGE" .

# 3. Run it, sandboxed, with the injected port published to loopback only.
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" \
  -p "127.0.0.1:${PORT}:7681" \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --pids-limit 256 \
  --memory 512m \
  "$IMAGE" >/dev/null

# 4. Wait until ttyd actually answers, so draiver's confirm-up healthcheck never races
#    bring-up and the revealed URL is live.
i=0
while [ "$i" -lt 100 ]; do
  if curl -fsS -o /dev/null "http://127.0.0.1:${PORT}/" 2>/dev/null; then
    echo "up: ttyd review terminal live on ${PORT} (instance ${INSTANCE}, ticket DEMO-033)"
    exit 0
  fi
  i=$((i + 1))
  sleep 0.1
done

echo "up: ttyd never answered on ${PORT}; container logs:" >&2
docker logs "$NAME" >&2 || true
exit 1
