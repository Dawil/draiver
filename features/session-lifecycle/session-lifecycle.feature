@drvctl-060 @cli
Feature: Supervised sessions know who they are, wait on their own background work, and stay stopped when a human interrupts
  Three lifecycle fixes in how the supervisor starts, judges and resumes a session.

  The daemon now hands every session DRAIVER_ACTOR=agent:<adapter> and
  DRAIVER_ATTEMPT=<id>, so an agent's `draiver` writes are attributed to it without
  an export prefix — and a write inside a session that would fall back to a human
  actor is refused. The completion gate treats background tasks the agent launched
  (run_in_background, async subagents) as "still working" rather than a stalled
  hand-off. And a human interrupt is a durable hold: the attempt is not auto-resumed
  until the human acts.

  Rule: A supervised session's writes are attributed to its agent

    Scenario: A supervised draiver log without exports is attributed to the agent
      Given a drvctl-060 attempt with a supervised session environment from the daemon
      When the agent runs draiver log with no exports of its own
      Then the logged note is attributed to agent:claude-code

    Scenario: A session write that would be attributed to a human is refused
      Given a drvctl-060 attempt with a supervised session environment from the daemon
      When the agent runs draiver log with DRAIVER_ACTOR overridden to a human
      Then the write is refused with an attribution error and nothing is appended

  Rule: Background tasks keep a session "still working"

    Scenario: A session with a pending background task is not halted
      Given a drvctl-060 completion gate on a session that launched an async subagent
      When the session ends its turn without a hand-off
      Then the gate waits — no nudge, no escalation and no halt
      And the subagent's completion notice clears the outstanding task

  Rule: A human interrupt holds the attempt until the human acts

    Scenario: An interrupted session is not resumed
      Given a drvctl-060 enabled attempt whose session the human interrupted
      When the supervisor ticks several times
      Then the interrupted attempt is not brought back up
      And a message from the human to the agent releases the hold

    @ui
    Scenario: The board shows an interrupted attempt as interrupted by human
      Given a drvctl-060 enabled attempt whose session the human interrupted
      Then the board shows the attempt Pending as interrupted by human
