@drvctl-055 @manager-as-worker
Feature: Park pending own async work — detached test, then park and wake
  drvctl-055 lets a worker fire a long `draiver test` off as a tracked async job
  and `park` instead of idling a live agent (and burning its context) while the
  test runs. The launch registers an async-job wake source on the worker's OWN
  log; the detached runner lands a durable `test-result` there when it finishes,
  which path-activates the parked worker to claim review (green) or iterate (red).
  This is the clearest demonstration that `park` is not manager-specific — a plain
  leaf worker suspends behind the same seam, with no `wants:` edge.

  Rule: A detached test in flight is a legitimate suspend point, not a human question

    Scenario: A launched-but-unanswered detached test makes the worker parkable
      Given a worker that launched a detached test and has not yet seen a result
      Then the worker has a pending async-job wake source
      And the worker is a suspend point rather than a human escalation

    Scenario: The landed result answers the source so the worker path-activates
      Given a worker whose detached test has landed its result on its own log
      Then the async-job wake source is answered and no longer holds the worker parked

  Rule: The completion gate auto-parks a handoff-less turn-end with a live async job

    Scenario: A turn that ends with a test still running parks instead of escalating
      Given a worker that ended its turn with a detached test running but filed no hand-off
      When the completion gate considers the turn-end
      Then the gate parks the worker rather than escalating a human
      And a park hand-off is recorded on the worker's own log

  Rule: A synchronous test result is not an async doorbell

    Scenario: A worker with only a sync test result has no async wake source
      Given a worker with a synchronous test result and no detached launch
      Then the worker has no async-job wake source
