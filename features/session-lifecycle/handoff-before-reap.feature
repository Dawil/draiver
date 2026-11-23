@drvctl-061 @cli
Feature: A session hands off before the supervisor reaps it, and restarts once on its own
  When the supervisor stops a session — at the context limit, or on a human's
  `ctl stop` — it first asks the live session to log a structured handoff note
  (`draiver log <ticket> --type note --tag handoff`) and waits up to
  stop_grace_seconds for it. After a successful handoff at the context limit, the
  supervisor resolves its own escalation and restarts the attempt on a fresh session
  cold-started from the brief, once (context_auto_restarts). A second trip on the
  same attempt is left for a human.

  These scenarios drive a REAL reconciler over a real git repo with a fake agent
  session; the fake session writes its handoff note by shelling the REAL `draiver`
  binary with the environment the supervisor gave it.

  Rule: The context-limit auto-stop checkpoints, then restarts once

    Scenario: A session at the context limit hands off and restarts on a fresh session
      Given a drvctl-061 supervised session with a context limit and one auto-restart
      When the drvctl-061 session's context crosses the limit
      Then the supervisor asks the session for a handoff note before reaping it
      And the handoff note lands on the log attributed to agent:claude-code
      And the supervisor resolves the auto-stop and restarts the attempt on a fresh session
      And the fresh session's brief carries the handoff note

    Scenario: A second trip on the same attempt escalates to a human
      Given a drvctl-061 supervised session with a context limit and one auto-restart
      When the drvctl-061 session's context crosses the limit
      And the restarted drvctl-061 session's context crosses the limit again
      Then the second auto-stop is left open for a human and nothing restarts
      And the board shows the drvctl-061 attempt waiting on a human

    Scenario: stop_grace_seconds 0 reaps immediately, as before
      Given a drvctl-061 supervised session with the handoff disabled
      When the drvctl-061 session's context crosses the limit
      Then the session is reaped at once without a handoff prompt and the escalation stays open

  Rule: ctl stop checkpoints through the supervisor that owns the session

    Scenario: ctl stop asks the session for a handoff note before reaping it
      Given a drvctl-061 supervised session owned by a running ctl up
      When a human runs ctl stop on the drvctl-061 attempt
      Then the stop reports a handoff note written before the reap
      And the stopped drvctl-061 attempt is not brought back up

    Scenario: ctl stop --now reaps without a handoff
      Given a drvctl-061 supervised session owned by a running ctl up
      When a human runs ctl stop --now on the drvctl-061 attempt
      Then the session is reaped without a handoff prompt
