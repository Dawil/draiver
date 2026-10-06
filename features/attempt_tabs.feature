# drv-021 — Attempt page: tabbed top section + collapsible bordered Agent Logs.
#
# These scenarios are the executable statement of the ticket's acceptance criteria
# (spec.md ## Acceptance criteria). After merging main's BDD/acceptance rung
# (resolution #23), they run under `go run ./features/acceptance`
# (internal/acceptance): each scenario drives a real internal/web server over a
# seeded attempt, asserts the server-rendered tabbed-card contract, and attaches a
# real Playwright screenshot of the live webui as its artefact — so draiver captures
# a genuine BDD report for this ticket (closing the #19/#23 gap).
#
# The written-for-humans Background/Scenario-Outline form was flattened to the
# subset the runner-agnostic reader supports (Feature/Scenario + Given/When/Then);
# true client-side switching, keyboard a11y, and the lazy-SSE toggle remain proven
# by the Playwright e2e in e2e/tests/attempt-tabs.spec.ts and the Go template tests
# in internal/web, which this rung complements with rendered-page evidence.

Feature: Attempt page tabbed top section

  Scenario: The top section is a tabbed card with the Spec tab selected by default
    Given an attempt detail page with every tab present
    Then the top section renders as a single bordered tabbed card with an accessible tablist
    And the Spec tab is selected on load and its panel is the only one visible

  Scenario: Selecting a tab reveals its panel and hides the others
    Given an attempt detail page with every tab present
    When I select the Provenance settings tab
    Then that tab's panel is shown and the Spec panel is hidden, with no page reload

  Scenario: The tablist is an accessible roving-tabindex control
    Given an attempt detail page with every tab present
    Then only the selected tab is in the tab sequence and the rest carry tabindex -1
    And the tablist is wired for arrow, Home, and End key navigation

  Scenario: The Git controls tab follows its panel's gate
    Given a Done attempt with a base and a repo
    Then the Git controls tab is absent
    Given a Running attempt with a base and a repo
    Then the Git controls tab is present and selecting it shows the git panel

  Scenario: The BDD report tab appears only once a run is captured
    Given a Running attempt with no captured BDD run
    Then the BDD report tab is absent
    Given a Running attempt with a captured BDD run
    Then the BDD report tab is present and selecting it embeds the report

  Scenario: Agent Logs is a bordered card below the tabs, collapsed by default
    Given an attempt detail page with every tab present
    Then the Agent Logs section is a bordered card below the tabbed card, collapsed by default
    And the whole header row is the toggle target
    When I activate the Agent Logs header
    Then the Agent Logs section is expanded

  Scenario: The Log section remains last and keeps its behaviours
    Given a Review attempt detail page with every tab present
    Then the Log heading, compose box, and polling log region are the last section
    And the Review actions still show on the Review attempt
