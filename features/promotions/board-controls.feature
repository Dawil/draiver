@ui @promotions @drv-046
Feature: Drive promotion checks and ratify a ticket from the board card
  As a reviewer scanning the Review column
  I want to run each promotion check and mark a ticket done from the card itself
  So that I can progress a ticket to Done without opening the attempt page

  # drv-046 makes the board card the control surface: a chip per declared promotion
  # (grey until run), clickable to run its check in place with a spinner, and an
  # all-green "mark done" tick that ratifies the ticket straight into Done. Promotions
  # stay informational — the tick is a convenience, never a new gate on `done`.

  Scenario: A Review card shows a chip for every declared promotion, including the grey not-run state
    Given a Review attempt whose repo declares dev, staging and prod promotions
    And dev is green at HEAD, staging has failed, and prod has never been checked
    When I open the board
    Then the card shows a green dev chip, a red staging chip, and a grey not-run prod chip

  Scenario: Clicking a chip runs its check and flips it in place
    Given a Review attempt whose repo declares dev, staging and prod promotions
    And dev is green at HEAD, staging has failed, and prod has never been checked
    When I click the grey prod chip on the card
    Then the check runs and the card comes back with prod flipped green, without a full page reload
    And the chip carries the spinner and aria-busy mechanism shown while the check is in flight

  Scenario: An all-green card shows a mark-done tick that moves the ticket to Done
    Given a Review attempt whose every declared promotion is green at HEAD
    When I open the board
    Then the card shows the mark-done tick, distinct from the archive accept tick
    When I click the mark-done tick
    Then a done event is appended and the card moves to the Done column, without opening the attempt page

  Scenario: A non-green card shows no mark-done tick and refuses the done POST
    Given a Review attempt whose repo declares dev, staging and prod promotions
    And dev is green at HEAD, staging has failed, and prod has never been checked
    Then the card shows no mark-done tick
    And a done POST for that attempt is refused

  Scenario: Running a check records a started and a terminal line in System Logs
    Given a Review attempt whose repo declares dev, staging and prod promotions
    And dev is green at HEAD, staging has failed, and prod has never been checked
    When I click the grey prod chip on the card
    Then the attempt's System Logs show a started line and then a pass line for prod

  # decision #25: a one-click "Restart context window" control on the board card for a
  # ticket that auto-stopped on a full context window — resolve + restart from the
  # dashboard, no confirmation, without opening the attempt.
  Scenario: A context-window auto-stopped card shows a one-click restart-context control
    Given a card whose attempt auto-stopped on a full context window
    When I open the board
    Then the card shows a restart-context control, absent on a card with an ordinary escalation
    When I click the restart-context control
    Then the escalation is resolved and the card no longer shows the control, without opening the attempt page
