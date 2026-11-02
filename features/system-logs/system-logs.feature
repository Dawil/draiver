@ui @drv-045 @system-logs
Feature: System Logs is a second streaming panel, separate from Agent Logs, stamped with real recorded times

  These scenarios are the executable statement of drv-045's acceptance criteria. They
  run under `go run ./features/acceptance` (internal/acceptance): each stands up a real
  internal/web server over a seeded attempt whose session dir already holds an agent
  stream (stream.jsonl), a health log (ctl.jsonl), and the new system-event journal
  (system.jsonl) carrying one record for each of the five operator/daemon sources, and
  then reads the live SSE panels the browser opens and captures a Playwright screenshot
  of both panels as evidence.

  The exact emit sites — that editing provenance, a git control, a review-env up/down,
  and a promotion check each append exactly one system.jsonl record with the right
  class — are pinned by the unit rung (internal/web systemlog_test.go); the
  render-from-recorded-ts and the agent/system split are pinned by internal/streamlog
  and internal/systemlog. This rung complements them with rendered-page evidence of the
  two-panel flow a reviewer actually sees.

  Scenario: The attempt page shows two separate live-log panels
    Given an attempt with seeded agent, health, and system logs
    Then the attempt page shows both an Agent Logs panel and a System Logs panel on their own stream routes

  Scenario: System Logs renders the system events with their real recorded timestamps
    Given an attempt with seeded agent, health, and system logs
    When I open the System Logs panel
    Then the System Logs panel shows the provenance, git, review-env, and promotion events
    And each System Logs line is stamped with its own recorded time, not the current clock

  Scenario: Agent Logs carries only the agent session stream
    Given an attempt with seeded agent, health, and system logs
    When I open the Agent Logs panel
    Then the Agent Logs panel shows the agent prose without any ctl health or session-lifecycle lines

  Scenario: System Logs refills after its journal is deleted
    Given an attempt with seeded agent, health, and system logs
    When the system.jsonl journal is deleted and a new system event is recorded
    Then the System Logs panel starts empty and shows only the refilled event
