@ui @drv-042
Feature: Board page sends Cache-Control: no-store so Back renders fresh state

  These scenarios are the executable statement of drv-042's acceptance criteria.
  Clicking the browser Back button from an attempt (ticket detail) page to the main
  dashboard used to restore the browser's bfcache DOM snapshot — stale board state
  shown for up to ~3s until the first htmx poll landed, confusing users and breeding
  mistrust in the board.

  The fix sends `Cache-Control: no-store` on the full board page response
  (`handleBoardPage`). `no-store` makes the page ineligible for the browser's
  back/forward cache, so pressing Back re-requests `/`, the handler re-renders
  current data, and there is no stale snapshot. The ≤3s gap to the next poll still
  exists, but the data shown on arrival is already correct.

  This rung drives the real internal/web server and asserts the server-observable
  contract — the response header, its handler-scoping, and the unchanged 3s/morph
  poll — and attaches a Playwright screenshot of the live board. The dynamic
  bfcache-eviction proof (a real Back navigation re-fetching in a browser) is a
  browser-level concern `no-store` is the documented, reliable lever for.

  Rule: The full board page defeats bfcache without disturbing the poll

    Background:
      Given a board and a running attempt detail page

    Scenario: The board page response carries Cache-Control: no-store
      Then the board page response carries the Cache-Control no-store header so Back re-fetches fresh

    Scenario: The no-store header is scoped to the full-page handler, not the shared renderer
      Then the board partial response does not carry the Cache-Control no-store header

    Scenario: The 3s morph poll is unchanged by the no-store header
      Then the board region still polls every 3s and swaps via morph, unchanged
