---
section: Added
---
**Reconcile loop — the draiverctld daemon that ties drvctl-001–007 together
(`draiverctl` Tier 0).** `internal/reconcile` is the supervisor's active half:
a **declarative** control loop that each tick reads the *desired* set (attempts
whose derived control state is `Running` — Needs-me/Review/Done are not desired)
and the *actual* set (sessions this daemon runs, plus live processes it
re-adopted), and closes the gap. Four moves. **Admit** — a desired attempt with
no live session gets one: `manage.Handle` Spawns a fresh session, or **Resumes**
the surviving cattle handle when a `session.json` is already on record; a single
ingest goroutine is attached to the stream and the cold-start brief is injected.
**Watch** — that goroutine tees every line to the journal, meters
tokens/cost/context, and promotes semantic events into the durable log
(`internal/watch`). **Gate** — the same goroutine runs both enforcement seams on
each tool-permission callback, protocol → permission (withhold-until-logged,
then allow-or-escalate-and-halt), so they never double-answer. **Retire** — an
attempt that left the desired set is stopped: a terminal one (Review/Done) has
its worktree cleaned, a blocked one (Needs-me) is **parked** with its worktree
kept warm — so a human `resolve` flips it back to `Running` and the next tick
re-admits it via Resume with no work lost (escalate → resolve → resume through
the reconcile diff alone, no path-activation needed at Tier 0). **Re-adoption on
restart** — `Adopt` rebuilds the view from disk (`session.json` per attempt) plus
a scan of the process table: it reconciles worktrees, re-adopts every recorded
session whose pid is still alive so a restart does **not** spawn a duplicate into
a live agent's worktree, and leaves dead sessions to be resumed on the next tick.
Adopting a live pid without re-streaming it is deliberate — the `agent.Adapter`
seam has no attach verb (that is the `Runtime.Attach` seam, Tier 1+), and a ctld
restart must not reap healthy agents; `daemon-reexec`, not a fleet bounce. A
`Proc` seam (signal-0 liveness + SIGTERM) makes re-adoption deterministically
testable. The client surface lands as `draiver ctl up` (run the loop until
interrupted; Ctrl-C **drains** — detaches without reaping, so the fleet survives
the daemon) and `draiver ctl status` (the desired/actual board). Respawn policy,
the progress watchdog, StartLimit ceilings, budgets, the dependency DAG, and
instant path-activation are all Tier 1+ and deliberately out of scope. Also adds
`manage.Handle.Decide`, the passthrough the two gates use to answer the live
adapter's permission callback (drvctl-008).
