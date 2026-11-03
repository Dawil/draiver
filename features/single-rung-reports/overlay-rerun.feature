@drv-047 @report @overlay
Feature: The board Rerun is overlay-aware
  As a reviewer on the board whose codebase carries no in-repo .test-pyramid.yaml
  I want the Rerun button to re-execute the overlay's bound BDD rung
  So that I can regenerate the acceptance evidence for an overlay-only codebase

  # drv-047 gap 1: bddexec predated overlays (drv-041) — it resolved its rung with a
  # bare in-repo pyramid.Load, so an overlay-only codebase hid the button and a forced
  # rerun returned no-rung, and even with a rung the overlay env vars were never set.
  Scenario: Rerun on an overlay-only codebase regenerates the report with both env vars exported
    Given an overlay-only codebase whose overlay pyramid has a bound BDD rung
    Then the board shows the Rerun button for the overlay-only attempt
    When I rerun the acceptance rung from the board for the overlay-only codebase
    Then the overlay rung ran with cwd the codebase and both overlay env vars exported
    And a fresh artefact set is captured for the overlay-only attempt
