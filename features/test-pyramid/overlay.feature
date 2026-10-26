@test-pyramid @overlay
Feature: Test-pyramid overlays from a separate, developer-owned repo
  As a solo, local-first developer whose team has not adopted draiver
  I want my test-pyramid tooling to live in a separate repo pointed at only via settings
  So that nothing is added to the shared codebase's version control to run a pyramid against it

  Rule: Resolution — in-repo wins, else an overlay, else a successful no-op

    Scenario: A codebase with no in-repo pyramid runs its overlay's pyramid
      Given a codebase worktree with no in-repo pyramid
      And an overlay registered for it whose pyramid records the run environment
      When draiver test runs for the attempt
      Then the overlay pyramid ran with cwd the codebase and both overlay env vars exported
      And the attempt provenance records the overlay repo and the commit used
      And the overlay's main branch is left untouched

    Scenario: An in-repo pyramid is used and the overlay is ignored
      Given a codebase worktree with an in-repo pyramid
      And an overlay registered for it whose pyramid records the run environment
      When draiver test runs for the attempt
      Then the in-repo pyramid ran and the overlay was never used

    Scenario: A codebase with neither is a successful no-op
      Given a codebase worktree with no in-repo pyramid
      And no overlay registered for it
      When draiver test runs for the attempt
      Then draiver test succeeds reporting nothing to test

  Rule: create-overlay is a deterministic, idempotent opt-in

    Scenario: create-overlay scaffolds, registers, and refuses on re-run
      Given a codebase worktree with no in-repo pyramid
      And a global overlay_dir is configured
      When draiver test create-overlay runs for the attempt
      Then an overlay repo is scaffolded under the overlay_dir and registered for the codebase
      And re-running create-overlay refuses and writes nothing
