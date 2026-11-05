@ui @promotions @drv-049
Feature: The promotion-check spinner is durable server state that survives the board poll
  As a reviewer watching a promotion check run from the board
  I want the chip spinner to stay visible for the whole check
  So that a check that outlasts the 3s poll never reads as "nothing happened"

  # drv-049 fixes a structural bug: the spinner used to live purely on htmx's transient
  # .htmx-request class, which the board's 3s idiomorph poll reconciled away mid-check —
  # a qa run longer than 3s lost its spinner within one tick. The fix makes "check in
  # progress" a DURABLE, server-rendered state: a `promotion-check` started marker on the
  # attempt log folds (project.DerivePromotions) into a `run` chip the server re-emits on
  # every render, so the morph reconciles TO the spinning chip rather than away from it.
  # A terminal verdict (or a blocked check's `cleared` marker) settles it, and an
  # abandoned marker is reconciled past a TTL so a killed check never spins forever.
  #
  # Like drv-046's board-controls rung, these scenarios drive the real internal/web
  # server and assert the server-rendered contract. The fix is structural — the spinning
  # chip IS in the server fragment every poll reconciles to — so the morph-survival
  # property is proven structurally here rather than by catching a sub-second browser flash.

  Scenario: An in-progress check renders a spinning chip from server state, independent of htmx-request
    Given a Review attempt whose repo declares a dev promotion
    And a promotion check for dev is in progress
    When I open the board
    Then the dev chip renders the spinning run state from server markup
    And the spinner reveal does not rely on the transient htmx-request class

  Scenario: The spinning chip survives every board poll re-render
    Given a Review attempt whose repo declares a dev promotion
    And a promotion check for dev is in progress
    Then every board poll re-render keeps drawing the spinning dev chip

  Scenario: A terminal verdict clears the in-progress state and the chip settles
    Given a Review attempt whose repo declares a dev promotion
    And a promotion check for dev is in progress
    When the dev check records a passing verdict at HEAD
    Then the dev chip settles to green and no longer spins

  Scenario: An abandoned in-progress marker does not leave a chip spinning forever
    Given a Review attempt whose repo declares a dev promotion
    And an abandoned promotion check for dev whose process died before any verdict
    When I open the board
    Then the dev chip does not spin and settles to its not-yet-checked state

  Scenario: The attempt-page Check button runs with no confirm dialog and shows in-progress feedback
    Given a Review attempt whose repo declares a dev promotion
    When I open the attempt page promotion tab
    Then the dev Check button carries no confirmation dialog
    And the dev Check button carries the in-progress spinner mechanism
