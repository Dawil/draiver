@drvweb-023 @manager-signal
Feature: The board surfaces the manager signal
  A running coordinator looks like any running ticket today, and the control
  plane's oscillation is invisible. drvweb-023 reads the spine's own wake reason
  (the `woken by C #N` activation event drvctl-051 records) and derives the queue
  depth, rendering a `wants:` ticket as a distinct coordinator card — "manager — on
  it, re: C1 · N queued" — plus the manager's own action line in the capability
  panel. It also fixes the board bug where an "escalated to `P`" reason rendered
  literal backticks.

  Scenario: A woken coordinator renders as a distinct card with its wake reason and queue depth
    Given a board with a woken coordinator and queued child doorbells
    Then the board renders the coordinator card marked distinct from a running ticket
    And the coordinator card shows the wake reason and the derived queue depth

  Scenario: The capability panel shows the manager's own current action
    Given a board with a woken coordinator and queued child doorbells
    Then the capability panel shows the manager's own current action line

  Scenario: An escalated-to reason renders as markdown, not literal backticks
    Given a board with a child escalated up to a pre-digest coordinator
    Then the board renders the escalated reason as markdown with no literal backticks
