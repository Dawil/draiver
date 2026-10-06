# drv-022 — Git controls: Sync always reports an outcome; the panel shows the
# current branch + ahead/behind vs the primary remote.
#
# These scenarios are the executable statement of the ticket's acceptance criteria
# (spec.md ## Acceptance criteria). They run under `go run ./features/acceptance`
# (internal/acceptance): each drives a real internal/web server over a seeded
# attempt backed by a real git repo + worktree + origin remote, performs the actual
# Sync button click in a live browser (accepting its hx-confirm), and attaches a
# Playwright screenshot of the resulting panel as its artefact — so draiver captures
# genuine before/after evidence of all three Sync outcomes and the position line.
#
# The precise banner kind/text and the ahead/behind computation across every case
# (up-to-date, ahead, behind, diverged, and the degradation fallbacks) are pinned by
# the comprehensive unit rung (internal/web git_controls_test.go, gitpos_test.go);
# this rung complements them with rendered-page evidence of the user-visible flow.

Feature: Git controls Sync always reports an outcome and shows branch position

  Scenario: Sync reports a successful pull-and-back-merge
    Given a Running attempt whose base has advanced past the branch
    When I Sync from the Git controls panel
    Then the Sync reports a success outcome in the panel

  Scenario: Sync reports an explicit no-op when nothing has moved
    Given a Running attempt already up to date with its remote
    When I Sync from the Git controls panel
    Then the Sync reports an already-up-to-date no-op in the panel

  Scenario: Sync reports a failure banner instead of a blank panel on a dirty checkout
    Given a Running attempt whose checkout is dirty
    When I Sync from the Git controls panel
    Then the Sync reports a failure outcome in the panel without a 500

  Scenario: The panel shows the current branch and position versus the primary remote
    Given a Running attempt ahead of its primary remote
    Then the Git controls panel shows the current branch and how far ahead/behind the remote it is
