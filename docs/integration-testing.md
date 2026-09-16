# Integration testing (the `integration` pyramid rung)

draiver dogfoods its own test-pyramid feature (drv-009). The repo ships a
checked-in [`.test-pyramid.yaml`](../.test-pyramid.yaml) with two rungs:

| Rung          | Command                                              | Needs Docker? |
| ------------- | --------------------------------------------------- | ------------- |
| `unit`        | `go test ./...`                                      | no            |
| `integration` | `go test -tags=integration ./internal/e2e/integration/...` | **yes**  |

`integration` is the **target (top) rung**, so drv-009's Review gate requires it
green-at-HEAD before a claim is accepted on this repo's own tickets.

## What the integration rung does

Unlike the unit suite — which fakes a remote with a local bare repo and never
starts a server — the integration rung stands up **real dependencies** with
[Testcontainers](https://testcontainers.com/):

- a real **Forgejo/Gitea** forge container, to exercise the loosely-coupled-forge
  coupling (`ctl merge --remote` containment: push, fetch, `merge-base
  --is-ancestor`) for real instead of mocking a remote; and
- the real **served `draiver webui` binary**, driven over HTTP against
  reproducibly-seeded data roots.

The harness lives in [`internal/e2e/integration`](../internal/e2e/integration).
A scenario declares what it needs and the pieces compose independently — a
ctl-only test takes a forge + a git repo + a data root; a webui test takes a
seeded data root + the served binary and no container at all. See the package doc
(`doc.go`) for the composition model.

## Running it locally

You need a container runtime (Docker or Podman) with its daemon running. Then:

```sh
# via the pyramid runner (builds the same command from .test-pyramid.yaml)
draiver test integration

# or directly
go test -tags=integration ./internal/e2e/integration/...
```

The first forge run pulls the `gitea/gitea` image, so it is slower; subsequent
runs reuse the cached image.

### No Docker? It skips cleanly

Every container-backed test calls `RequireContainerRuntime(t)`, which **skips**
(does not fail) with an actionable message when no runtime is reachable. So on a
machine without Docker the container-free webui scenarios still run and only the
forge scenarios skip — and the `unit` rung, which carries none of this (the whole
package is behind the `//go:build integration` tag), is never blocked by a missing
daemon.

## Determinism & teardown

Containers and the served process are bound to the test via `t.Cleanup` and
Testcontainers' `Terminate`/Reaper; data roots and git repos live under
`t.TempDir()`. A pass or a fail leaves nothing behind — a leaked container or
volume is a harness fault, not a test outcome. Each scenario seeds its own data
root, git repo, and forge repo, so no scenario leaks into the next.
