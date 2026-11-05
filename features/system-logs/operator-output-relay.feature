@ui @drv-048 @system-logs
Feature: Operator-action subprocess output is relayed into the System Logs panel

  These scenarios are the executable statement of drv-048's acceptance criteria: the
  stdout/stderr an operator action's underlying command produces — a promotion check's
  qa, a git control's report, a review-env bring-up — must reach the System Logs panel,
  not be discarded or truncated to a one-line summary. Each drives a REAL internal/web
  server over a seeded attempt through the genuine action path (handlePromote →
  promote.Run, handleGitVerb → land.Sync, handleReviewEnvUp → reviewenv.Launch) and then
  reads the attempt's system.jsonl — the same journal the panel tails — to prove the
  relayed output landed, each line with its own recorded timestamp.

  The per-line relay mechanics (buffering, the first-line split, the up/down tagging)
  are pinned by the unit rung (internal/web systemlog_test.go, internal/reviewenv
  reviewenv_test.go); this rung complements them with end-to-end evidence that the output
  a reviewer needs to diagnose a failure actually reaches the stream.

  Scenario: A promotion check's output lands between its started and pass lines
    Given a Review attempt whose prod promotion check prints diagnostic output
    When I click the grey prod chip on the card
    Then the System Logs show the prod check's output between the started and pass lines

  Scenario: A git Sync relays its full report, not just the truncated first line
    Given a Running attempt whose base has advanced past the branch
    When I Sync from the Git controls panel
    Then the System Logs show the full Sync report beyond its one-line summary

  Scenario: A review-env bring-up relays the up command's output
    Given a Review attempt whose review environment prints bring-up output
    When I bring the review environment up from the board
    Then the System Logs show the environment's bring-up output
