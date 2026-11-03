@drv-047
Feature: draiver test --only runs and captures one rung on demand
  As an agent iterating on a BDD scenario, or a reviewer wanting a fresh report
  I want to run and capture exactly one rung without climbing the whole ladder
  So that a report can be produced on demand without the gate going green

  # drv-047 gap 2: levelsUpTo always returns the base→target prefix and climb halts at
  # the first red required rung, so a high rung could not run while a lower required one
  # was red, and the acceptance report could not be regenerated off the --log-green path.
  Scenario: --only captures the acceptance report while the required unit rung below is red
    Given a codebase whose required unit rung fails and whose acceptance rung is green
    When I run draiver test acceptance --only for the attempt
    Then the acceptance rung ran green and exits zero
    And the acceptance report is captured into the attempt artefact store
    And no test-result gate event is recorded

  # Soundness: --only is an evidence producer, --log is the gate recorder; combining them
  # would let a single-rung run masquerade as a gate pass, so it is refused.
  Scenario: --only and --log together are refused with a clear message
    Given a codebase whose required unit rung fails and whose acceptance rung is green
    When I run draiver test acceptance --only --log for the attempt
    Then the run is refused and explains a single-rung run cannot record a gate result
