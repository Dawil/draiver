@drvctl-059 @agent-messages
Feature: draiver commands announce human messages posted during a live agent session
  The spine delivers human input to a *stopped* agent (a reopen or resolution wakes a
  resumed session, and its resume note names what changed), but nothing reached a
  session that was already running: a message the human posted mid-session sat in the
  log unseen, and once the agent claimed review it was lost for good (exap-023 #12).

  Messages the board compose form addresses to the agent carry an explicit `to: agent`
  marker. The daemon hands each session the log seq it started at; marked messages
  above it, and above the session's read cursor, are unread. Every agent-facing
  `draiver` command then opens with a one-line, count-only notice on stderr, and
  `draiver messages` reads them. Only `review` — the point of no return — is
  soft-stopped until the agent reads them or deliberately passes `--ignore-unread`.

  Rule: A message posted mid-session is announced, count-only, on stderr

    Scenario: The next agent command prints the unread notice
      Given a supervised agent session is live on an attempt
      When the human posts a message to the agent from the board
      And the agent logs a note
      Then the agent's command prints the unread notice with a count of 1 on stderr
      And the agent's command stdout and exit code are unchanged
      And the notice carries the `draiver messages` pointer and no message content

    Scenario: Reading the messages clears the notice until the human posts again
      Given a supervised agent session is live on an attempt
      And the human has posted a message to the agent from the board
      When the agent runs draiver messages
      Then the message is printed in full
      And the agent's next command prints no unread notice
      When the human posts two more messages to the agent from the board
      And the agent logs a note
      Then the agent's command prints the unread notice with a count of 2 on stderr

  Rule: Only messages the session has not been given count

    Scenario: A message posted before the session started is covered by the brief
      Given an attempt with a message to the agent posted before any session
      When a supervised agent session starts on it
      And the agent logs a note
      Then the agent's command prints no unread notice

    Scenario: Spine and system notes never count, whatever their actor
      Given a supervised agent session is live on an attempt
      When spine and system notes stamped with human actors land on the attempt
      And the agent logs a note
      Then the agent's command prints no unread notice

    Scenario: Outside a supervised session commands never print the notice
      Given an attempt with a message to the agent posted before any session
      When a human runs draiver log outside any session
      Then the agent's command prints no unread notice

    Scenario: An error computing the notice changes nothing
      Given a supervised agent session is live on an attempt
      And the human has posted a message to the agent from the board
      And the session's read cursor file is unreadable
      When the agent logs a note
      Then the agent's command prints no unread notice
      And the agent's command stdout and exit code are unchanged

  Rule: review is soft-stopped by unread messages

    Scenario: review with unread messages records nothing until read or ignored
      Given a supervised agent session is live on an attempt
      And the human has posted a message to the agent from the board
      When the agent claims review
      Then the review is refused and no event is appended
      And the refusal prints the unread notice and the --ignore-unread hint
      When the agent claims review with --ignore-unread
      Then the review claim is recorded

    Scenario: review after reading the messages is recorded as today
      Given a supervised agent session is live on an attempt
      And the human has posted a message to the agent from the board
      When the agent runs draiver messages
      And the agent claims review
      Then the review claim is recorded

  Rule: The exap-023 replay

    Scenario: draiver test announces the message and review is soft-stopped
      Given a supervised agent session is live on an attempt
      When the human posts a message to the agent from the board
      And the agent runs draiver test
      Then the test output carries the unread notice at the top and again at the end
      When the agent claims review
      Then the review is refused and no event is appended

    Scenario: A message ignored past review is listed by the next resume note
      Given a supervised agent session is live on an attempt
      And the human has posted a message to the agent from the board
      When the agent claims review with --ignore-unread
      And the human reopens the attempt with a decision
      Then the next resume note lists the unread message

  Rule: The human escalation inbox is untouched

    Scenario: draiver inbox behaves exactly as before
      Given a supervised agent session is live on an attempt
      When the human posts a message to the agent from the board
      Then draiver inbox prints the same output as before the message and no notice

  Rule: The board shows which entries are messages to the agent

    Scenario: The compose form marks entries as messages to the agent
      Given a supervised agent session is live on an attempt
      And the human has posted a message to the agent from the board
      When the human opens the attempt page
      Then the compose form offers a "Message the agent" box checked by default
      And the posted message carries a "to agent" badge in the log
