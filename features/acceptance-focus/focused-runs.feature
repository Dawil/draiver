@cli @drv-063
Feature: Focused acceptance runs
  While iterating, an agent runs only its own scenarios — by tag, by capability folder,
  or by scenario name — instead of the whole Playwright suite. A focused run is
  evidence for the agent, never a gate result: `draiver test` runs only the named
  rung, records no test-result and captures no report, and refuses --log/--detach.

  Background:
    Given a drv-063 scratch attempt whose acceptance rung is the real runner over two tagged capability folders

  Scenario: draiver test acceptance --tags runs only the tagged scenarios and records nothing
    When I run draiver test acceptance focused on the drv-946 tag
    Then only the drv-946 scenarios ran
    And the lower unit rung did not run
    And the drv-063 attempt has no test-result recorded

  Scenario: draiver test acceptance --dir runs only one capability folder
    When I run draiver test acceptance focused on the beta folder
    Then only the beta folder scenarios ran
    And the drv-063 attempt has no test-result recorded

  Scenario: The runner's --name filter selects scenarios by name
    When I run the acceptance runner directly with a name filter for "shown"
    Then only the scenarios named shown ran

  Scenario: A focused run refuses to record a gate result
    When I run draiver test acceptance focused on the drv-946 tag with --log
    Then the focused run is refused because it can never be a gate result
    And the drv-063 attempt has no test-result recorded
