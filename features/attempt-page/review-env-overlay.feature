@ui @review-env @overlay @drv-053
Feature: Review environments honour test-pyramid overlays
  As a reviewer of a codebase whose test pyramid lives in a separate overlay repo
  I want Launch on the attempt page to run the overlay's review-env scripts at the attempt's overlay revision
  So that the environment I click around is the one the agent defined, and nothing leaks when it fails or is torn down

  Scenario: An in-repo pyramid's review env gets no overlay env vars
    Given a codebase in Review with an in-repo review env and an overlay also registered
    When I launch the "review" review environment from the web UI
    Then the in-repo review env is up and its scripts saw no overlay env vars

  Rule: An overlay-only codebase's review env runs with the overlay execution contract

    Background:
      Given an overlay-only codebase in Review whose overlay declares its review env via $DRAIVER_OVERLAY_DIR
      And the attempt's overlay branch changes the review env's healthcheck and URL

    Scenario: Launching from the web UI brings the overlay review env up with both overlay env vars
      When I launch the "review" review environment from the web UI
      Then the review env is up with the overlay branch's URL on the panel
      And its scripts saw DRAIVER_CODEBASE_DIR as the codebase review worktree and DRAIVER_OVERLAY_DIR as an overlay checkout at the overlay branch tip

    Scenario: The overlay branch's definition is used, not overlay main
      When I launch the "review" review environment from the web UI
      Then the launched env uses the overlay branch's healthcheck and URL, not overlay main's

    Scenario: Teardown replays the overlay env and removes both review worktrees
      Given the "review" review environment was launched from the web UI
      When I tear the "review" review environment down from the web UI
      Then down and the confirm-down healthcheck reran with the same overlay env
      And the env is confirmed down and both review worktrees are removed

    Scenario: A failing up runs down and leaves no worktrees behind
      When I launch the "broken" review environment from the web UI
      Then the launch failed, down ran with the overlay env, and no review worktree is left
