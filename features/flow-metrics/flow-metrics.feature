@ui @flow-metrics @drvweb-027
Feature: Flow-metrics strip above the board, with an age-sort toggle on Oldest
  As an operator scanning the board
  I want four at-a-glance flow numbers above the columns and a one-click age sort
  So that I can see how the flow is doing and surface the oldest work without leaving the board

  # drvweb-027 adds a flow-metrics strip top-right above the columns, inside the 3s-polled
  # board region: Oldest (age of the oldest unresolved code ticket, and its name), Expected
  # Dev-Cycle (p85 of resolved created→done durations), Total Unresolved (the population behind
  # Oldest), and 4-Week Throughput (tickets done within 28 days). All four are computed over
  # CODE tickets only — archived attempts and codebase ledgers are excluded. The Oldest tile is
  # also the age-sort toggle: clicking it re-sorts each column oldest-first WITHIN each favourite
  # partition (composing with drv-050), cookie-backed so the order survives the poll.

  Scenario: The strip renders four tiles top-right above the columns inside the polled region
    Given a flow board seeded with a spread of code tickets, a ledger, and an archived attempt
    When I open the flow board
    Then the flow-metrics strip renders above the columns with all four tiles
    And the strip sits inside the 3s-polled board region

  Scenario: Oldest and Total Unresolved describe one population, excluding ledgers and archived
    Given a flow board seeded with a spread of code tickets, a ledger, and an archived attempt
    When I open the flow board
    Then Oldest names the oldest unresolved code ticket and shows its age
    And Total Unresolved counts every unresolved code ticket
    And neither the ledger nor the archived attempt is counted in Oldest or Total Unresolved

  Scenario: Expected Dev-Cycle is the p85 of resolved durations, with an em dash below the floor
    Given a flow board seeded with a spread of code tickets, a ledger, and an archived attempt
    When I open the flow board
    Then Expected Dev-Cycle shows a humanised duration
    And a board with fewer than three resolved tickets shows an em dash for Expected Dev-Cycle

  Scenario: 4-Week Throughput counts only tickets done within the last 28 days
    Given a flow board seeded with a spread of code tickets, a ledger, and an archived attempt
    When I open the flow board
    Then 4 Week Throughput counts only the resolved tickets done within 28 days

  Scenario: Clicking Oldest toggles age-sort, lights the pressed state, and survives the poll
    Given a flow board seeded with a spread of code tickets, a ledger, and an archived attempt
    When I click the Oldest tile
    Then the columns sort oldest-first and the Oldest tile shows its pressed state
    And the pressed age-sort order survives a repeated board poll

  Scenario: Age-sort orders oldest-first within each favourite partition and off restores the default
    Given a flow board with favourited and non-favourited cards of varied ages
    When I turn age-sort on
    Then every favourite still sits above every non-favourite
    And each favourite partition is independently ordered oldest-first by age
    When I turn age-sort off
    Then the columns return to favourites-first then alphabetical

  Scenario: The metric math is deterministic under a fixed clock
    Given the flow-metric computation
    Then it is proven deterministic by a fixed-clock unit test covering the 28-day boundary
