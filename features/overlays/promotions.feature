@overlays @promotions @drv-044
Feature: Promotions compose with test-pyramid overlays
  As a developer whose codebase's pyramid lives in a separate overlay repo
  I want to opt the repo into an overlay from the CLI and promote it from the board
  So that an overlay-declared promotion works off every surface, not just `draiver promote`

  # drv-044 closes the three gaps that stopped a promotion declared in an overlay from
  # working: the CLI could not set a per-repo overlay, and the attempt-page promote
  # executor ignored overlays (offering a button guaranteed to fail). Promotions remain
  # a separate, informational axis — they never gate review or done.

  Scenario: draiver config repo --overlay sets, clears, and reads the per-repo overlay
    Given a repo with an isolated operator config
    When I run draiver config repo with --overlay pointing at an overlay repo
    Then config.json records the explicit per-repo overlay
    And the value written is byte-for-byte what the web repo-settings panel writes
    When I run draiver config repo with --overlay cleared
    Then config.json no longer records a per-repo overlay
    When I run draiver config repo with no flags
    Then the read form prints the resolved overlay alongside remote, branch, and rung

  Scenario: The attempt-page Check button runs an overlay-only promotion green
    Given a Review attempt whose repo has no in-repo pyramid but an overlay declaring a promotion
    When I click the attempt-page Check button for the overlay's environment
    Then the promotion runs the overlay qa with the overlay script-env and returns a real pass
    And a commit-bound promotion-result pass is recorded, not a no-promotion
    And the board-triggered promotion records the overlay provenance a reviewer can see
    And the attempt page shows the overlay promotion promoted at HEAD
