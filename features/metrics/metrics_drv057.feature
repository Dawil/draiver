@cli @metrics @drv-057
Feature: draiver metrics — `cd`-aware path resolution and a --split-at commit cohort
  As an operator measuring whether one change (say an AGENTS.md edit) helped agents
  I want scratch work outside the repo kept out of the metrics, and a clean
  before/after split on the commit that made the change
  So that the numbers describe the codebase, not /tmp, and compare like with like

  # The world (separate from the drv-054 worlds):
  #   widget-d  a repo; main is C0 (AGENTS.md v1), then C1 rewrites AGENTS.md and is
  #             tagged agents-md-change — the split commit
  #   drv-910   cut from C0 (before). Its transcript scratches in /tmp after a `cd`
  #             (`cd /tmp/…; cat > a.md`, `cd /tmp/… && sed -i … a.md`,
  #             `cd /tmp/… && cat notes.md`), writes src/a.md via `cd src && cat > a.md`,
  #             and leaves a throwaway notes.md at the repo root. It ships src/a.md.
  #   drv-911   cut from C1 (after): the same transcript without the throwaway notes.md
  #   drv-912   cut from C1 (after): as drv-911
  #   drv-913   on widget-d with no branch and no recorded base (unknown)

  Background:
    Given a drv-057 metrics world split by an AGENTS.md change on widget-d

  Scenario: A cd earlier in a Bash command moves where its relative paths resolve
    When I run draiver "metrics drv-910 --compute --json"
    Then drv-910 counts no file written or read under /tmp after a cd
    And drv-910 resolves `cd src && cat > a.md` to the shipped src/a.md
    And drv-910's only transient file is the throwaway notes.md at the repo root

  Scenario: Rows stored under the previous metrics_version read as stale
    When I run draiver "metrics drv-910 --compute --json"
    Then the row is computed under metrics_version 3
    Given drv-910's stored record is rewritten as metrics_version 2
    When I run draiver "metrics drv-910 --json"
    Then the row reads as stale

  Scenario: --split-at reports the aggregate per cohort, with unknown bases excluded
    When I run draiver "metrics --codebase widget-d --compute --split-at agents-md-change --json"
    Then the before cohort is drv-910 and the after cohort is drv-911 and drv-912
    And drv-913 is excluded from both cohorts as an unknown base
    And each cohort carries its own aggregate: transient_files median 1 before and 0 after

  Scenario: --split-at in the human codebase view
    When I run draiver "metrics --codebase widget-d --compute --split-at agents-md-change"
    Then the output shows the before and after cohorts and the excluded unknown base

  Scenario: --split-at works with --by file
    When I run draiver "metrics --codebase widget-d --compute --split-at agents-md-change --by file --json"
    Then each cohort ranks AGENTS.md with its own reader count
    When I run draiver "metrics --codebase widget-d --stored --split-at agents-md-change --by file"
    Then the output shows a file hotspot table per cohort

  Scenario: --split-at refuses a commit the repo does not have
    When I run draiver "metrics --codebase widget-d --split-at no-such-commit" expecting failure
    Then it fails naming the unknown --split-at commit
