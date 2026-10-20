# ttyd recall review env (drv-032)

A browser terminal for reviewing the `draiver recall` changes on this branch
(`--tags`, the `ticket@attempt` address form, and the attempt-awareness header)
without installing anything locally.

It is distinct from the sibling drv-020 **webui** review env (`review-env/up.sh`):
those changes are UI, these are **CLI**, so this serves a real shell — a
[`ttyd`](https://github.com/tsl0922/ttyd) web terminal — over the reviewed
`draiver` binary and a pre-seeded board.

## Use

```sh
review-env/ttyd/up.sh          # build + run; prints the URL when ttyd is live
# open http://127.0.0.1:7681/ and follow the on-login suggestions
review-env/ttyd/down.sh        # stop + remove the container
```

Override the host port with `DRAIVER_TTYD_PORT=8800 review-env/ttyd/up.sh`.

## What's seeded

One ticket, **`DEMO-033`**, with four attempts (`0001`–`0004`) whose facts carry
a varied, reusable tag set (`env-reset`, `plan`, `shell-escape`, `approach`,
`demo`) and one supersession on the latest attempt. That is enough to exercise
every change: `recall DEMO-033 --tags`, `recall DEMO-033@0001`, the attempt
header on a bare `recall DEMO-033`, and `--all` / `--chain` over the superseded
fact. The on-login MOTD lists the exact commands to try.

The board is left **writable** inside the container, so you can also `log` /
`--supersedes` new facts and watch `recall` reflect them. The container is
ephemeral — `down.sh` discards all of it.

## How it's built

`up.sh` host-builds the reviewed `draiver` (static, `CGO_ENABLED=0`) and seeds
the board into `review-env/ttyd/.build/` (gitignored), then the `Dockerfile`
just layers `ttyd` + that binary + that data onto `debian:bookworm-slim` — so
the image needs no Go toolchain and the build only hits the network for `apt`
and the `ttyd` release. On an x86_64 host pass `--build-arg TARGETARCH=amd64`.

## Sandbox

Non-root user, `--cap-drop ALL`, `--security-opt no-new-privileges`,
`--pids-limit`/`--memory` caps, and the port published to `127.0.0.1` only (not
reachable off this host).
