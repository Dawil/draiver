# drv-021 — Attempt page: tabbed top section + collapsible bordered Agent Logs.
#
# These scenarios are the executable statement of the ticket's acceptance criteria
# (spec.md ## Acceptance criteria). draiver's own repo ships no godog/cucumber BDD
# rung (its .test-pyramid.yaml is unit → integration), so the realisation of these
# scenarios lives as deterministic tests rather than step definitions:
#   • the Go template tests in internal/web — TestAttemptTabsStructure,
#     TestGitControlsGating, TestCachePanelRendersMetrics, TestProvenanceInlineEditTable,
#     TestAttemptDetailRendersSpecAndTimeline (the unit rung `draiver test` gates on); and
#   • the Playwright e2e in e2e/tests/attempt-tabs.spec.ts (client-side switching,
#     keyboard a11y, the Agent Logs affordance, and the new-UI screenshots).
# Each scenario names, in its comments, where it is proven.

Feature: Attempt page tabbed top section
  As a reader of an attempt's detail page
  I want the top-of-page panels folded into one tabbed card, defaulting to the Spec
  So that only the panel I care about costs vertical space above the live Log

  Background:
    Given I am on an attempt's detail page

  # internal/web TestAttemptTabsStructure; e2e "renders a tabbed card with Spec selected by default"
  Scenario: The top section is a tabbed card with the Spec tab selected by default
    Then the top section renders as a single bordered tabbed card
    And the tablist exposes role "tablist" with role "tab" controls
    And the Spec tab is selected (aria-selected="true") on load
    And the Spec panel is visible while every other panel is hidden

  # e2e "selecting each tab reveals its panel and hides the rest, with no reload"
  Scenario Outline: Selecting a tab shows its panel and hides the others, with no reload
    When I click the "<tab>" tab
    Then the "<tab>" panel is shown and marked aria-selected="true"
    And every other tab panel is hidden
    And the page did not reload or round-trip to the server

    Examples:
      | tab                 |
      | Spec                |
      | Provenance settings |
      | Git controls        |
      | BDD report          |
      | Cache details       |

  # e2e "tabs are keyboard-operable: focus the tablist, arrow keys move selection"
  Scenario: Tabs are keyboard-operable with a roving tabindex
    When I focus the tablist and press the Right arrow
    Then focus and selection move to the next tab
    And aria-selected tracks the active tab
    And only the active tab is in the tab sequence (tabindex="0"); the rest are tabindex="-1"
    And Left arrow, Home, and End move selection as an accessible tablist

  # internal/web TestGitControlsGating + TestAttemptTabsStructure
  Scenario: The Git controls tab follows its panel's existing gate
    Given the attempt has no base or repo, or is Done
    Then the Git controls tab is absent
    But when the attempt has both a base and a repo and is not Done
    Then the Git controls tab is present

  # internal/web report tests + e2e "conditional tabs follow their panel's gate"
  Scenario: The BDD report tab appears only once a run is captured
    Given the attempt has no captured BDD run
    Then the BDD report tab is absent
    But when a BDD run has been captured for the attempt
    Then the BDD report tab is present

  # e2e "Agent Logs is a bordered card below the tabs, collapsed by default, header toggles it"
  Scenario: Agent Logs is a bordered card below the tabs, collapsed by default
    Then the Agent Logs section renders as a bordered card below the tabbed card
    And it is collapsed by default, with no live stream open
    And the whole header row — not just the label — is the toggle target
    When I activate the Agent Logs header
    Then the section expands and the live session stream begins
    When I activate the header again
    Then the section collapses and the stream closes

  # e2e "the Log heading and entries remain the last section and still poll";
  # internal/web TestAttemptDetailRendersSpecAndTimeline
  Scenario: The Log section remains last and keeps its behaviours
    Then the "Log" heading, compose box, and log timeline are the last section
    And the log region still polls the live fragment every 3 seconds
    And the Review actions still show on a Review attempt
    And polling the log region never disturbs the selected tab
