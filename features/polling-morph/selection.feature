@ui @drvweb-025
Feature: Polling regions morph instead of clobbering selection

  These scenarios are the executable statement of drvweb-025's acceptance criteria.
  The tickets board and the attempt detail page both self-poll every 3s. They used
  to swap with hx-swap="innerHTML", which replaces the region's whole DOM subtree and
  destroys any live text selection — you could not copy text out of the board or the
  live log without the next tick wiping it.

  Both regions now swap via morph (the vendored idiomorph extension): the incoming
  fragment is diffed against the live DOM and only changed nodes are mutated, so an
  unchanged poll makes zero mutations and a selection, caret, or :target on an
  untouched node survives. This rung drives the real internal/web server, asserts the
  server-rendered morph contract, and attaches a Playwright screenshot of the live
  webui. The dynamic proof — that a real selection/caret/:target actually survives a
  3s tick in a browser — is held by the Playwright e2e in e2e/tests/{detail,board}.spec.ts,
  which this rung complements with rendered-page evidence.

  Rule: Both polling regions morph rather than replace

    Background:
      Given a board and a running attempt detail page

    Scenario: The board region morphs on its 3s poll
      Then the board region polls with a morph swap, not an innerHTML replace
      And the idiomorph extension is served locally and enabled, with no external script

    Scenario: The attempt live region morphs on its 3s poll
      Then the attempt live region polls with a morph swap, not an innerHTML replace
      And the live fragment still carries its out-of-band state badge and log count

  Rule: Morph does not disturb the poll lifecycle or leave dead workarounds

    Scenario: A terminal attempt still self-cancels its poll
      Given a Done attempt detail page
      Then the live endpoint answers 286 so htmx stops polling

    Scenario: The innerHTML-era selection workarounds are retired
      Given a board and a running attempt detail page
      Then the detail page ships no innerHTML-era focus-snapshot or is-target workaround
