---
section: Added
---
**Cattle handle — kill/keep a session id and `--resume` a fresh process
(`draiverctl` Tier 0, hook 1).** `internal/manage`'s `Handle` binds one
attempt's agent adapter, its git worktree, and its persisted `session.json`
identity into a single live-session controller — the "agents are cattle"
mechanic in one type. `Spawn` creates the worktree, starts a fresh agent
process, and persists its session id (with pid, worktree, birth time). `Kill`
reaps the process but **keeps the session id and the ticket log**, clearing only
the now-stale pid — context lives in the log, not the dead process. `Resume`
reloads the session id from `session.json` and spins a fresh process with the
agent's `--resume`, re-attached to the same worktree (`Create` re-attaches to
the surviving branch if a restart swept the checkout); it keeps the id,
worktree, and birth time and refreshes only the pid. Each `Spawn`/`Resume`
yields a new process, so `Stream()` returns a fresh channel for the caller to
re-attach its `Watcher` to. `manage` owns identity only; the meter and stream
tee stay with `internal/watch`. pid is read via an optional `PID()` accessor
(added to the Claude Code adapter) so the core `agent.Adapter` seam stays the
five control verbs (drvctl-005).
