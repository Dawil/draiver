@drvctl-062 @agent-messages @cli
Feature: Human events reach a running session as a turn, and review waits for them
  drv-044: the human logged review feedback meant for another ticket and retracted
  it five minutes later. The live session never saw the retraction and built the
  wrong feature; `draiver log` gave it no hint. drv-051: the human logged a decision
  mid-session and the agent claimed review without having seen it.

  A human decision, resolution, supersede — or any entry addressed to the agent —
  is now a deliverable event. The supervisor injects each one into the live session
  as a short turn quoting its text, queued for the session's next turn boundary.
  The agent's own events are never echoed back. `draiver log` and `draiver review`
  name the unread events by seq and kind, and review refuses until the agent
  acknowledges them with `draiver messages` or `review --ack`.

  Rule: The supervisor pushes human events into the live session

    Scenario: A supersede appended mid-session reaches the session as a turn before its next tool call
      Given a drvctl-062 session is live under a real supervisor
      And the human logged a decision that was pushed to the session
      When the human supersedes that decision mid-session
      And the drvctl-062 supervisor ticks
      Then the session received the supersede as a turn before its next tool call

    Scenario: The agent's own events and spine notes are not echoed back
      Given a drvctl-062 session is live under a real supervisor
      When the agent logs a decision and the spine logs a note mid-session
      And the drvctl-062 supervisor ticks
      Then the session received no pushed turn

  Rule: Writes name unread human events, and review waits for an acknowledgement

    Scenario: review refuses with an unread human event, then accepts after ack
      Given a supervised agent session is live on an attempt
      When the human logs a decision on the board without messaging the agent
      And the agent logs a note
      Then the agent's write names the unread decision by seq and kind
      When the agent claims review
      Then the review is refused and no event is appended
      When the agent claims review with --ack
      Then the acknowledged decision is printed in full
      And the review claim is recorded

  Rule: The resume note carries the human delta

    Scenario: A human decision never read before review is listed by the next resume note
      Given a supervised agent session is live on an attempt
      When the human logs a decision on the board without messaging the agent
      And the agent claims review with --ignore-unread
      And the human reopens the attempt with a decision
      Then the next resume note lists the unread message
