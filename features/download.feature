Feature: Download the standalone BDD report
  As a reviewer on the board
  I want to download the BDD report as a single self-contained file
  So that I can read the evidence offline, away from the board

  Scenario: The downloaded report is a self-contained file
    Given a captured BDD run with an inlined screenshot
    When I render the standalone report for download
    Then the report is a single self-contained HTML document
    And the screenshot is inlined in the downloaded file
