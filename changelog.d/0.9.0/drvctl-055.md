---
section: Added
---
**Park pending your own async work — detached `draiver test --detach` plus
worker park/wake (drvctl-055).** A worker running a long test used to hold a live
agent idling (context and token burn) or stop with no handoff, which the
completion gate turned into a **false escalation** a human had to discern and
resolve. The pattern is now `draiver test --detach && draiver park`:
**`--detach`** requires a clean tree, writes a new **`async-job`** launch marker
on the attempt's own log, spawns a `setsid`-detached runner (the hidden
`__detached-test` verb, logging to `detached/test-NNNN.log`) and returns at once;
when the climb finishes it lands a durable **`test-result`** carrying a new
`Outcome` (`pass`/`fail`) and an artefact pointer. `wake.Pending` gains an
**`AsyncJob`** source, so the parked worker shows as **Pending**, not Needs-me.
A new reconciler pass (`internal/reconcile/async_jobs.go`,
`reconcileAsyncJobs`) holds it Pending while the job is in flight and, when the
result lands, authors a `woken-by` (reusing drvctl-051's path activation, with
the worker's own ticket as the woken child) to cold-start it with the result as
the brief tail — to claim review on green or iterate on red. `launch && park` is
safe: a failed launch short-circuits `&&` (and answers its own marker), and
park's no-wake-source refusal (drvctl-052) backs it up. As a safety net, the
completion gate **auto-parks** a handoff-less turn-end when a live async job
exists instead of escalating. Retire now keeps the worktree warm while an
`AsyncJob` is in flight (the detached climb runs there), and `draiver review`'s
green gate ignores `Outcome: fail` results. The handbook and onboarding skill
gain a new §8 documenting the `launch-async && park` pattern; BDD coverage lives
in `features/manager-as-worker/park-async.feature`.
