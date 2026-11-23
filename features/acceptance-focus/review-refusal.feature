@cli @drv-063
Feature: A refused review says why
  When `draiver review` is refused for a red required rung, the refusal names the
  rung and its failing test, and says whether that rung was already red at the
  merge-base with the base branch — inherited from main — or green there, so the
  branch introduced it.

  Background:
    Given a drv-063 attempt whose unit rung fails TestInheritedRed
    And a detached climb has recorded the red unit rung

  Scenario: A red rung that was already red on main is reported as inherited
    Given main's own attempt recorded the unit rung red at the merge-base
    When I claim review on the drv-063 attempt
    Then the review is refused naming rung unit and failing test TestInheritedRed
    And the refusal says the acceptance rung was not reached
    And the refusal says the red rung is inherited from main

  Scenario: A red rung that was green on main is reported as introduced by the branch
    Given main's own attempt recorded the acceptance rung green at the merge-base
    When I claim review on the drv-063 attempt
    Then the review is refused naming rung unit and failing test TestInheritedRed
    And the refusal says this branch introduced the failure
