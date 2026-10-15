@test-pyramid
Feature: Per-rung required decouples the review gate from altitude
  As a repo owner
  I want advisory test rungs the review gate does not require
  So that a flaky or unavailable rung never blocks a claim while altitude ordering stays pure

  Rule: The gate folds the required set, not a single top rung

    Scenario: An advisory rung above the required set never blocks the gate
      Given a pyramid with an advisory integration rung above required unit and acceptance
      And the required rungs unit and acceptance are logged green at HEAD
      When the review gate evaluates the attempt
      Then the gate admits the claim with integration never run

    Scenario: A missing required rung refuses the claim and names it
      Given a pyramid requiring unit and acceptance
      And only unit is logged green at HEAD
      When the review gate evaluates the attempt
      Then the gate refuses the claim and names acceptance outstanding

  Rule: A required rung may sit above an advisory one and still gate

    Scenario: A required rung above an advisory one gates across it
      Given a pyramid with required e2e above an advisory integration rung
      And unit and e2e are logged green at HEAD
      When the review gate evaluates the attempt
      Then the gate admits the claim across the advisory rung
