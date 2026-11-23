@ui @drv-066 @agent-logs
Feature: Agent log lines carry their recorded event time, not render-time wallclock

  These scenarios are the executable statement of drv-066's acceptance criteria.
  Replaying a finished attempt used to stamp every agent-stream line with the time it
  was rendered, so a historical log showed "now" on every line. The renderer now stamps
  each line with the record's own stream-json "timestamp" (shown local); records that
  carry none (system/init, result) inherit the last recorded time seen before them.

  Each scenario seeds an attempt whose stream.jsonl holds timestamped assistant/user
  records from a far-past day, then reads it back through the real surfaces: the built
  `draiver ctl logs` binary (non-follow) and the web Agent Logs panel's SSE backlog,
  with a Playwright screenshot of the panel as evidence. The fallback-to-now and
  inheritance edge cases are pinned by the unit rung (internal/streamlog).

  Scenario: ctl logs replays a recorded attempt with its recorded event times
    Given a finished attempt whose agent stream carries recorded timestamps
    When I replay it with draiver ctl logs
    Then every agent line shows its recorded time, not the current time

  Scenario: The web Agent Logs panel backlog shows the recorded event times
    Given a finished attempt whose agent stream carries recorded timestamps
    When I open the Agent Logs panel for the recorded attempt
    Then every agent line shows its recorded time, not the current time
