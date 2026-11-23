@ui @drvweb-029 @attempt-page
Feature: Copy a "fork this session" command from an attempt

  An operator often wants to ask an attempt's agent something without the exchange
  reaching the draiver log or the agent's own session. Claude Code's
  `claude --resume <id> --fork-session` opens an interactive copy of the
  conversation under a NEW session id, leaving the original transcript untouched.
  The attempt page offers that command, ready to paste, built server-side from the
  attempt's session.json: an absolute `cd` into the worktree, the session id, and
  `--fork-session --permission-mode plan` so the fork cannot edit the worktree it
  shares with the live agent.

  The quoting and gating rules (spaces and quotes in paths, empty session id,
  relative worktree, other adapters) are pinned by the unit rung
  (internal/web fork_test.go); this rung adds rendered-page evidence.

  Scenario: A claude-code attempt with a session shows the copy button and command
    Given a board attempt with a seeded claude-code session.json
    Then the attempt page shows the fork-session copy button
    And the copy button sits inline in the Agent Logs section header
    And the rendered fork command cds into the absolute worktree and forks the session in plan mode

  Scenario: An attempt with no session id shows no copy button
    Given a board attempt whose session.json has no session id
    Then the attempt page shows no fork-session copy button
