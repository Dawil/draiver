@drvctl-057 @manager-as-worker
Feature: Async-job liveness-reap — wake a worker whose detached runner died
  drvctl-055 lets a worker fire `draiver test --detach && draiver park` and suspend
  to Pending behind a single wake source: the detached runner will land a
  `test-result` on its own log and path-activate it. The runner records a terminal
  result on every clean return path, so a clean exit always answers the launch. But a
  runner that spawns and then dies or hangs before recording one — SIGKILL, a
  `timeout` wrapper, OOM, a host reboot, a rung wedged on a server that never comes up
  — lands no result at all, and the worker would sleep in Pending forever with a dead
  waker. drvctl-057 is the automatic half of the recovery the operator `park --clear`
  does by hand: once a parked worker's launch is overdue past a deadline, the
  reconciler force-answers it with the same terminal fail result, so the existing
  doorbell path wakes the worker to iterate.

  The reconciler's deadline decision and the `woken-by` activation that re-admits the
  woken worker are exercised against the real reconciler at the unit rung
  (internal/reconcile/async_jobs_test.go), mirroring drvctl-055's split. Here we pin
  the agent-and-human-facing contract over the real wake and review-gate engines: an
  overdue dead launch is force-answered with a gate-safe fail so the worker wakes
  rather than sleeps, and a launch still inside the deadline is never cut short.

  Rule: An overdue launch whose runner never landed a result is reaped automatically

    # The dual of park --clear, firing on its own: no operator needs to notice the
    # stuck worker. The reaped result carries Outcome=fail, so it both wakes the worker
    # (a doorbell, like any result) and can never satisfy the review gate.
    Scenario: The reaper force-answers an overdue launch so the worker wakes to iterate
      Given a worker parked behind a detached test whose runner died without a result
      When the reaper force-answers the overdue launch
      Then the async-job wake source is answered and no longer holds the worker parked
      And the cleared result is a fail the review gate does not count as green

  Rule: A detached runner still within the reap deadline is not reaped mid-flight

    # No false reap (AC #4): the deadline sits comfortably above a real acceptance
    # climb, so a worker legitimately mid-climb keeps its live wake source and waits
    # for the genuine verdict.
    Scenario: A launch inside the deadline keeps the worker parked, unreaped
      Given a worker parked behind a detached test still within the reap deadline
      Then the worker has a pending async-job wake source
      And no terminal result has been fabricated for the launch
