@drv-026 @progressive-brief
Feature: The cold-start brief scales with live facts, not total events
  Cold-start cost used to be O(total log length), re-paid on every respawn. The
  progressive fold pushes only a bounded index of live facts, so a busy ticket's
  brief tracks the live-fact count — superseded history is free.

  Scenario: Superseded history does not bloat the brief
    Given a busy attempt with many superseded facts
    Then the brief is materially smaller than a full-log replay
    And adding more superseded history keeps the brief size flat
