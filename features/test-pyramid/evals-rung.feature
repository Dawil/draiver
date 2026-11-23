@drv-065
Feature: Agent-context evals are an advisory, environment-bound evals rung
  As a repo owner measuring whether docs changes help agents
  I want evals declared as an ordinary advisory test-pyramid level
  So that they run only on demand, never gate review, and block rather than false-green

  # The rung under test is a stub `run:` — no real agent — so these scenarios prove the
  # pyramid contract draiver already offers (required: false, environment:, artifacts:,
  # --only) is all an evals suite needs. draiver has no eval-specific interface.

  Scenario: A bare draiver test does not climb into the advisory evals rung
    Given a codebase whose pyramid has a stub advisory evals rung above a required unit rung
    When I run a bare draiver test in the evals codebase
    Then the unit rung ran and the evals rung did not

  Scenario: draiver test evals --only runs the rung and reports its artefact without a gate result
    Given a codebase whose pyramid has a stub advisory evals rung above a required unit rung
    When I run draiver test evals --only in the evals codebase
    Then only the evals rung ran and the run exits zero
    And the evals report is captured into the attempt artefact store
    And the evals run records no test-result gate event

  Scenario: A box without the agent CLI blocks the evals rung instead of passing it
    Given a codebase whose stub evals rung needs an agent CLI this box does not have
    When I run draiver test evals --only in the evals codebase
    Then the evals rung is blocked by its environment healthcheck and never runs

  Scenario: This repository declares its evals rung advisory, top-most and agent-bound
    Given this repository's own test pyramid
    Then its evals rung is the top level, advisory, and bound to an environment that requires the claude CLI
    And its evals rung declares its report directory as an artifact
