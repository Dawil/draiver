@drv-047 @report
Feature: BDD reports are viewable outside Review
  As a reviewer
  I want a captured report to be viewable whatever state the attempt is in
  So that the report is tied to having evidence, not to the Review state

  # drv-047 gap 3: viewing a report is NOT gated on State == Review — the tab shows on
  # HasRuns alone. This locks that behaviour so it cannot silently regress into a state
  # gate; the on-demand producers (overlay Rerun, --only) make a report reachable here.
  Scenario: An attempt not in Review with a captured run shows and renders its report
    Given a Running attempt (not in Review) with a captured BDD run
    Then the attempt page shows its BDD report tab
    And the report route renders the captured report
