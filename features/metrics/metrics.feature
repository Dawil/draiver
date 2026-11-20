@cli @metrics @drv-054
Feature: draiver metrics — per-attempt and per-codebase agent-performance metrics
  As an operator deciding which evals to write
  I want metrics folded from what agents actually did, per attempt and per codebase
  So that I can see where a codebase makes agents explore, guess and stumble

  # Every scenario drives the real draiver binary over a scratch data root whose
  # attempts carry recorded stream.jsonl fixtures (features/metrics/fixtures/, values
  # tabulated in features/metrics/README.md) and seeded logs. The world holds:
  #   drv-901  widget-a clone, freshly cut (recorded provenance), in review,
  #            the hand-built widget transcript, model claude-fixture-1
  #   drv-902  widget-b clone of the SAME remote, legacy (no provenance), ratified,
  #            a lighter transcript, model claude-fixture-2
  #   drv-903  widget-a clone, no branch, escalated and unresolved, model claude-fixture-1
  #   drv-904  an unrelated repository, never selected by --codebase widget-a

  Background:
    Given a metrics world of fixture attempts across two clones of one remote and an unrelated repo

  Scenario: One attempt's metric families match the hand-built widget fixture
    When I run draiver "metrics drv-901 --compute --json"
    Then the drv-901 row reports the widget fixture's exploration and context metrics
    And the drv-901 row reports the widget fixture's error-kind split
    And the drv-901 row reports outcome in-review with a one-file final diff

  Scenario: Human output for one attempt shows every metric family and the transcript pointer
    When I run draiver "metrics drv-901 --compute"
    Then the output shows the provenance, outcome, totals, exploration and errors sections
    And the output names the transcript command "draiver ctl logs drv-901 --attempt 0001"

  Scenario: --stored never computes, --compute persists, --stored then returns the same values
    When I run draiver "metrics drv-901 --json"
    Then the drv-901 row source is "missing" and no metrics.json was written
    When I run draiver "metrics drv-901 --compute --json"
    Then the drv-901 row source is "computed" and metrics.json was written
    When I run draiver "metrics drv-901 --stored --json"
    Then the drv-901 row source is "stored" with values identical to the computed run

  Scenario: An older metrics_version makes the record stale, and --recompute recomputes everything
    When I run draiver "metrics --codebase widget-a --compute --json"
    And drv-901's stored record was computed under an older metrics_version
    And I run draiver "metrics drv-901 --json"
    Then the drv-901 row source is "stale"
    When I run draiver "metrics --codebase widget-a --recompute --json"
    Then every codebase row has source "computed"

  Scenario: --codebase selects by repo across two clones of one remote and groups by model
    When I run draiver "metrics --codebase widget-a --compute --json"
    Then the codebase rows are drv-901, drv-902 and drv-903 but not drv-904
    And the aggregate is grouped by model claude-fixture-1 and claude-fixture-2 with outcome counts
    And the aggregate lists the unread docs README.md and docs/guide.md

  Scenario: --sort and --limit return the worst N with transcript pointers
    When I run draiver "metrics --codebase widget-a --compute --sort exploration_prefix_calls --limit 2 --json"
    Then the codebase rows are the 2 worst by exploration_prefix_calls, each with its transcript pointer
    When I run draiver "metrics --codebase widget-a --stored --sort exploration_prefix_calls --limit 2"
    Then the output shows per-model aggregates and the worst-first table with transcript pointers

  Scenario: A newly cut attempt records provenance; legacy attempts report inferred or unknown
    When I run draiver "metrics --codebase widget-a --compute --json"
    Then drv-901's base_commit and repo_id are recorded and its model is observed
    And drv-902's base_commit is labelled inferred
    And drv-903's base_commit is unknown
