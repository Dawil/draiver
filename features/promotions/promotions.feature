@ui @promotions @drv-043
Feature: Surface environment promotions on the board
  As a reviewer deciding whether to ratify a ticket
  I want to see which environments a Review ticket is promoted-and-current in
  So that I know at a glance which tickets are ready to mark Done — without leaving the board

  # draiver never PERFORMS a promotion: it only VERIFIES one by running the repo-declared
  # `qa` success check, and surfaces the result as information. Promotions never gate
  # review or done.

  Scenario: A promotions section parses and an invalid promotion is rejected
    Given a test-pyramid declaring a promotion for a declared environment
    Then the promotions section parses and resolves the promotion by environment
    And a promotion naming an undeclared environment is rejected with a clear message
    And a promotion with a blank qa is rejected with a clear message

  Scenario: draiver test never climbs a promotion
    Given a test-pyramid with both levels and promotions
    Then the rungs draiver test would climb are exactly the levels, never a promotion

  Scenario: The Review column shows green and stale promotion labels
    Given a Review attempt whose repo declares promotions for dev and staging
    And dev is promoted green at HEAD and staging is promoted at an older commit
    When I open the board
    Then the Review card shows dev promoted-and-current and staging stale

  Scenario: The attempt page in Review offers a promote control per environment
    Given a Review attempt whose repo declares promotions for dev and staging
    When I open the attempt page promotions tab
    Then a promote control is offered for each declared environment
