@drv-026 @progressive-brief
Feature: Talk to the ticket attempt via recall
  The same fold + recall surface lets a Claude session interactively "talk to the
  attempt" out of band of draiver's supervised flow: it answers questions about the
  attempt by pulling from the immutable log on demand.

  Scenario: Answer questions about the attempt from the log on demand
    Given an attempt with decisions, gotchas, and a superseded fact
    Then recall answers which gotchas are known
    And recall answers the history of a superseded fact
