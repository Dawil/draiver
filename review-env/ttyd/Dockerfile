# review-env/ttyd/Dockerfile — a browser terminal (ttyd) over a draiver binary and a
# pre-seeded board, so a reviewer can drive the recall CLI changes (drv-032 #12) without
# installing anything locally.
#
# The draiver binary and the seeded data are built on the HOST by up.sh (which has go +
# git at the reviewed commit) and dropped into .build/ — the same "copy a pre-built binary"
# shape as the repo's Dockerfile.draiver. This image therefore needs neither go nor git,
# and the build only reaches the network for apt + the ttyd release.
#
# Build context is the worktree root. Build via up.sh, not `docker build` directly, so
# .build/ exists first.
FROM debian:bookworm-slim

ARG TTYD_VERSION=1.7.7
# arm64 host by default; override --build-arg TARGETARCH=amd64 on an x86_64 host.
ARG TARGETARCH=arm64

RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*

# ttyd static binary from tsl0922/ttyd (the project #12 names). arch asset map:
#   arm64 -> aarch64,  amd64 -> x86_64.
RUN set -eu; \
    case "$TARGETARCH" in \
      arm64) A=aarch64 ;; \
      amd64) A=x86_64 ;; \
      *)     A="$TARGETARCH" ;; \
    esac; \
    curl -fsSL -o /usr/local/bin/ttyd \
      "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.${A}"; \
    chmod +x /usr/local/bin/ttyd

COPY review-env/ttyd/.build/draiver /usr/local/bin/draiver
COPY review-env/ttyd/motd.sh        /etc/profile.d/00-draiver-motd.sh

# Non-root reviewer; the seeded board is copied into its home and left WRITABLE so the
# reviewer can also test `log`/`--supersedes` and see recall reflect it.
RUN useradd -m -s /bin/bash reviewer
COPY --chown=reviewer:reviewer review-env/ttyd/.build/data /home/reviewer/.draiver-data

USER reviewer
WORKDIR /home/reviewer
ENV DRAIVER_DATA=/home/reviewer/.draiver-data

EXPOSE 7681
# -W: writable (keystrokes reach the shell).  bash -l: login shell, so the MOTD shows.
ENTRYPOINT ["ttyd", "-p", "7681", "-W", "bash", "-l"]
