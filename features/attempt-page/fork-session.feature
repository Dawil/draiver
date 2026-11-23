@ui @drvweb-029 @attempt-page
Feature: Copy a "fork this session" command from an attempt

  An operator often wants to ask an attempt's agent something without the exchange
  reaching the draiver log or the agent's own session. Claude Code's
  `claude --resume <id> --fork-session` opens an interactive copy of the
  conversation under a NEW session id, leaving the original transcript untouched.

  The attempt page's Agent Logs header offers a ready-to-paste `draiver fork
  '<ticket>@<attempt>'`. The verb cds into the session's worktree (Claude Code keys
  transcripts by cwd), recreating it on the attempt's branch if the daemon
  reclaimed it, then runs `claude --resume <id> --fork-session --permission-mode
  plan`, so the fork cannot edit the worktree it shares with the agent.

  The quoting and gating rules (paths with spaces and quotes, a non-default data
  root, an empty session id, a relative worktree, other adapters) are pinned by the
  unit rung (internal/web fork_test.go, cmd fork_test.go); this rung adds
  rendered-page and end-to-end evidence.

  Scenario: A claude-code attempt with a session shows the copy button and command
    Given a board attempt with a seeded claude-code session.json
    Then the attempt page shows the fork-session copy button
    And the copy button sits inline in the Agent Logs section header
    And the rendered fork command is a draiver fork of this attempt

  Scenario: An attempt with no session id shows no copy button
    Given a board attempt whose session.json has no session id
    Then the attempt page shows no fork-session copy button

  Scenario: The copied command recreates a reclaimed worktree and forks the session in plan mode
    Given a claude-code attempt whose worktree the daemon has reclaimed
    When I run the fork command copied from the attempt page
    Then the worktree is recreated on the attempt branch
    And claude is started there with --resume, --fork-session and --permission-mode plan
