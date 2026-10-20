# ttyd CLI review env (drv-032)

A browser terminal for reviewing the `draiver recall` changes on this branch
(`--tags`, the `ticket@attempt` address form, and the attempt-awareness header)
without installing anything locally.

It is the **`review-cli`** environment declared in `.test-pyramid.yaml` — a
drv-020 `reviewEnvironment` draiver launches on demand from an attempt page in
Review. It is distinct from, and coexists with, the sibling webui `review` env
(`review-env/up.sh`): those changes are UI, these are **CLI**, so this serves a
real shell — a [`ttyd`](https://github.com/tsl0922/ttyd) web terminal — over the
reviewed `draiver` binary and a pre-seeded board, inside a sandboxed docker
container.

## Driving it from the board (the intended path)

`reviewEnvironment` is a single per-repo setting naming one env, so on the
attempt page's repo-settings panel, point **`reviewEnvironment` at
`review-cli`** (set it back to `review` for UI tickets). Then use the
review-environment panel on an attempt in Review to launch it: draiver cuts a
review worktree at the reviewed commit, injects a free `DRAIVER_REVIEW_PORT` and
a unique `DRAIVER_REVIEW_INSTANCE`, runs `up.sh`, confirms readiness with
`healthcheck.sh`, and only then reveals the URL. Teardown runs `down.sh` and
re-probes the port to confirm the env is gone.

## Running it manually

```sh
review-env/ttyd/up.sh          # build + run; prints the port when ttyd is live
# open http://127.0.0.1:7681/ and follow the on-login suggestions
review-env/ttyd/down.sh        # stop + remove the container
```

With neither `DRAIVER_REVIEW_PORT` nor `DRAIVER_REVIEW_INSTANCE` set (i.e. not
launched by draiver), `up.sh` falls back to port `7681` (override with
`DRAIVER_TTYD_PORT`) and a fixed instance name.

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
The container/image are tagged by `DRAIVER_REVIEW_INSTANCE`, so concurrent
review envs on sibling attempts never collide.

## Sandbox

Non-root user, `--cap-drop ALL`, `--security-opt no-new-privileges`,
`--pids-limit`/`--memory` caps, and the injected port published to `127.0.0.1`
only (not reachable off this host).
