@cli @metrics @drv-054
Feature: draiver metrics v2 — sessions, hotspots, invention and correctness fixes
  As an operator deciding what to change in a codebase
  I want deterministic metrics that point at the cause: context exhaustion, oversized
  or borrowed files, recurring commands and throwaway harnesses
  So that I can act on them without hand-written transcript scripts

  # The world (separate from metrics.feature's, so its selections stay untouched):
  #   widget-c  a repo with AGENTS.md, a 200-line src/big.go and src/steps_drv901.go
  #   drv-905   cut from widget-c, the sessions fixture (two sessions, three segments;
  #             features/metrics/fixtures/gen_sessions.py), a context-auto-stop
  #             escalation and a review on its log; ships src/big.go and
  #             src/zz_drv905_focus_test.go
  #   drv-906   the same transcript and log for ticket drv-906, cut from widget-c
  #   drv-801   legacy (no provenance) on the merged-repo fixture: main was merged
  #             into its branch, then the branch landed

  Background:
    Given a metrics v2 world with two restart-spanning attempts on widget-c and a merged-branch attempt

  Scenario: Sessions, segments and context figures come from the restart-spanning transcript
    When I run draiver "metrics drv-905 --compute --json"
    Then drv-905 reports 2 sessions, 3 segments, a 30000-token context floor and 1 compaction
    And drv-905's sessions ended by context-auto-stop then review, from the log
    And drv-905 splits its rereads into cross-session 1, same-range 1 and ranged 1
    And drv-905 reports active time 350s against wall time 2090s and a net exploration prefix of 10000

  Scenario: A CLI rejecting an unknown command counts as env discovery even when the exit was masked
    When I run draiver "metrics drv-905 --compute --json"
    Then drv-905 reports 1 env_discovery_error from `draiver promote` with a non-error result

  Scenario: Invention detection finds the transient file and the ticket-scoped focus test
    When I run draiver "metrics drv-905 --compute --json"
    Then drv-905 reports transient file tmp/notes.txt and ticket-scoped test src/zz_drv905_focus_test.go
    When I run draiver "metrics --codebase widget-c --compute --json"
    Then the codebase inventions group src/zz_*_test.go across drv-905 and drv-906

  Scenario: Human output for one attempt shows the sessions and invention sections
    When I run draiver "metrics drv-905 --compute"
    Then the output shows the v2 sections for drv-905

  Scenario: --by file ranks files by context tokens and names cross-ticket readers
    When I run draiver "metrics --codebase widget-c --compute --by file --json"
    Then the file hotspots rank src/big.go first with 18000 context tokens and 200 lines at HEAD
    And src/steps_drv901.go is read cross-ticket by drv-905 and drv-906
    When I run draiver "metrics --codebase widget-c --stored --by file --limit 2"
    Then the output shows the file hotspot table

  Scenario: --by command lists normalized commands recurring across attempts
    When I run draiver "metrics --codebase widget-c --compute --by command --json"
    Then the command hotspots include "draiver promote <ticket>" and "go test ./src/..." in 2 attempts each
    When I run draiver "metrics --codebase widget-c --stored --by command"
    Then the output shows the command hotspot table

  Scenario: Base-commit inference does not mistake main-merged-into-branch for the landing merge
    When I run draiver "metrics drv-801 --compute --json"
    Then drv-801's base_commit is inferred as main at the merge and its diff is its own one file

  Scenario: Computing the same attempt twice is byte-identical apart from computed_at
    When I run draiver "metrics drv-905 --recompute --json"
    And I run draiver "metrics drv-905 --recompute --json" again
    Then the two outputs are byte-identical apart from computed_at
