@ui @favourites @drv-050
Feature: Star a board card to float it to the top of its column
  As an operator scanning a busy board
  I want to pin the two or three tickets that matter right now to the top of their column
  So that the hot cards stay visible without changing anyone else's view

  # drv-050 adds a per-viewer "favourite" star to every board card, beside the archive X
  # and styled to match it. Clicking it floats the card to the top of its column; clicking
  # again unstars it. Favourites are a view preference of the person looking at the board,
  # not ticket state, so they live ONLY in that web user's cookie — the server persists
  # nothing. Within each column, favourites sort above non-favourites, alphabetical among
  # themselves; non-favourites keep the existing alphabetical default.

  Scenario: Every active card shows a hover-revealed star beside the archive X
    Given a board with three Running cards in alphabetical order
    When I open the board
    Then every card shows a star button in its action cluster beside the archive X
    And the star is hidden at rest and revealed on card hover or focus
    And a favourited card's star stays visible without hovering

  Scenario: Clicking the star floats the card to the top and stores its ref in the cookie
    Given a board with three Running cards in alphabetical order
    When I click the star on the last card
    Then that card moves to the top of its column
    And the favourites cookie now contains that card's ref
    When I click the star on that card again
    Then the card returns to its alphabetical position
    And the favourites cookie no longer contains that card's ref

  Scenario: Multiple favourites sort alphabetically above the non-favourites
    Given a board with three Running cards in alphabetical order
    When I favourite the last card and then the middle card
    Then the two favourited cards render above the non-favourited card
    And the two favourited cards are ordered alphabetically among themselves

  Scenario: The server stores no favourite state and the cookie drives identical ordering
    Given a board with three Running cards in alphabetical order
    When I favourite the last card
    Then a board render with no cookie shows the plain alphabetical order
    And the same cookie drives the identical favourites-first order across repeated renders
    And no durable favourite event was written to any attempt

  Scenario: A malformed or oversized favourites cookie never errors the board
    Given a board with three Running cards in alphabetical order
    When I open the board with a malformed favourites cookie
    Then the board renders successfully in the plain alphabetical order
