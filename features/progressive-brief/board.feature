@drv-026 @progressive-brief
Feature: The board faithfully displays superseded facts and tags
  The minimum webui change is faithful display: a superseded fact is demoted with a
  link to the event that retired it (reusing the resolved-escalation pattern), and
  an event's opaque tags render as chips. No existing board view breaks.

  Scenario: A superseded fact is demoted with a link, and tags show as chips
    Given a board attempt with a superseded fact and tags
    Then the attempt page demotes the superseded fact with a link to its successor
    And the attempt page shows the tags as chips
