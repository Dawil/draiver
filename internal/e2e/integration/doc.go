//go:build integration

// Package integration is draiver's Testcontainers-backed integration harness —
// the top ("integration") rung of the repo's own .test-pyramid.yaml (drv-010,
// dogfooding drv-009). It stands up *real* dependencies instead of fakes: a real
// Forgejo/Gitea forge container for the loosely-coupled-forge coupling, and the
// real served webui binary against reproducibly-seeded data roots — so scenarios
// exercise exactly what an operator runs, not a mock of it.
//
// # Build tag
//
// Every file here carries `//go:build integration`, so `go test ./...` (the unit
// rung) never compiles or runs it: the base rung stays fast and container-free.
// Run this rung explicitly:
//
//	draiver test integration
//	go test -tags=integration ./internal/e2e/integration/...
//
// # The composition model
//
// A scenario declares what it needs, and the pieces compose independently:
//
//   - A *data root* is a self-contained, reproducibly-seeded directory tree (the
//     spec's "data-root volume"): one per scenario, seeded fresh from a named
//     Fixture via the real CLI, so no scenario leaks into the next. See dataroot.go.
//   - A *git repo under test* is a local working tree seeded to a known commit
//     graph, so merge/sync cases are deterministic. See gitrepo.go.
//   - A *forge* is a Forgejo/Gitea container — the review forge under test — with
//     its own storage; a test seeds a repo and a "merged PR" state on it. See
//     forge.go.
//   - A *webui* is the served `draiver webui` binary bound to a free port over a
//     seeded data root, driven over HTTP. See webui.go.
//
// A ctl-only test (the forge round-trip) takes a forge + a git repo + a data
// root; a webui test takes a data root + the served binary and no container at
// all. Nothing forces a test to pay for a dependency it does not use.
//
// # Determinism and teardown
//
// Container and process lifetimes are bound to the test via t.Cleanup and
// Testcontainers' Terminate/Reaper, so a pass or a fail leaves nothing behind; a
// leaked container or volume is a harness fault, not a test outcome. Data roots
// and git repos live under t.TempDir(), removed by the test runner.
//
// # CI-friendliness
//
// The container-backed pieces call RequireContainerRuntime(t), which skips the
// test cleanly with a clear message when no Docker/Podman daemon is reachable —
// so a machine without a container runtime runs the container-free webui
// scenarios and skips only the forge ones, and the unit rung is never blocked.
package integration
