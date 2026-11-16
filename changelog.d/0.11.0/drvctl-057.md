---
section: Added
---
**Automatic liveness-reap for async jobs — a parked worker whose detached test
runner died is woken instead of sleeping forever (drvctl-057).** A worker that
runs `draiver test --detach && draiver park` (drvctl-055) waits on the detached
runner to land a `test-result`; a runner that spawned but then died or hung
(SIGKILL, a `timeout` wrapper, OOM, host reboot, a rung wedged on a server that
never comes up) landed nothing, and the reconciler re-trimmed the attempt to
Pending every tick indefinitely — until an operator ran `draiver park --clear`.
Now `reconcileAsyncWorker` (`internal/reconcile/async_jobs.go`), in its "parked
behind an unanswered launch" branch, checks whether the `async-job` launch is
older than a deadline and, if so, lands a terminal **`fail`** `test-result` via
`wake.AsyncLaunchFailResult` (refs the launch seq, spine actor, a body stating no
climb ran to a verdict). The existing `woken-by` path wakes the worker on the
next tick with no wake/activation code change; `Outcome: fail` keeps the review
gate from reading it as green. The deadline is a launch-timestamp check, not PID
liveness, so it survives reboots: new config key **`async_job_reap_minutes`**
(default `30`, ~6x a normal acceptance climb; `0` disables) maps to
`reconcile.Options.AsyncJobReapMaxAge`, mirroring `review_env_max_age_minutes`.
At most one result lands per launch, and a racing real result still wakes the
worker exactly once. The `wake.go` docs now list all three `AsyncLaunchFailResult`
callers (manual `park --clear`, spawn failure, automatic reap); acceptance
coverage in `features/manager-as-worker/async-reap.feature`.
