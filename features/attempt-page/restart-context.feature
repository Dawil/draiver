@ui @drv-028 @escalation
Feature: Restart context window on a context-window auto-stop escalation

  These scenarios are the executable statement of drv-028's acceptance criteria.
  Each drives a real internal/web server over a seeded attempt that is Stuck on an
  escalation, performs the actual button click in a live browser, and attaches a
  Playwright screenshot as its artefact — so draiver captures genuine evidence that
  the "Restart context window" affordance appears only on the auto-stop kind and that
  one click both clears the escalation and restarts the attempt on a clean window.

  The precise render guard and handler semantics (resolve-first durability, the
  non-auto-stop route rejection, the CSRF guard) are pinned by the unit rung
  (internal/web web_test.go); this rung complements them with rendered-page evidence.

  Rule: The button appears only on a context-window auto-stop escalation

    Scenario: An auto-stop escalation offers both Resolve and Restart context window
      Given a board attempt stuck on a context-window auto-stop escalation
      Then the escalation offers both Resolve and Restart context window

    Scenario: An ordinary escalation offers Resolve only
      Given a board attempt stuck on an ordinary escalation
      Then the escalation offers Resolve without a Restart context window button

  Rule: Clicking Restart context window resolves the escalation and restarts the attempt

    Scenario: One click resolves and restarts on a clean context window
      Given a board attempt stuck on a context-window auto-stop escalation
      When I click Restart context window
      Then the escalation is resolved and the attempt is restarted on a clean context window

    Scenario: A restart that cannot be scheduled still resolves the escalation durably
      Given a board attempt stuck on a context-window auto-stop escalation with no running controller
      When I click Restart context window
      Then the escalation is resolved durably and the restart failure is reported
