Feature: Rerun the BDD rung to regenerate the artefact
  As a reviewer on the board
  I want to rerun the bound acceptance rung from the report page
  So that the artefact evidence is regenerated without my leaving the board

  Scenario: Rerun on a green environment regenerates the artefact
    Given a bound BDD rung on a healthy environment
    When I rerun the acceptance rung from the board
    Then the rung runs green and a fresh artefact set is captured

  Scenario: Rerun is blocked when the environment healthcheck is red
    Given a bound BDD rung whose environment healthcheck is red
    When I rerun the acceptance rung from the board
    Then the rerun is blocked and no bogus report is captured
    And the block explains that the ball is passed back to the human
