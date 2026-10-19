@report
Feature: Order the BDD report's tag facets alphabetically
  As a reviewer reading a BDD report
  I want the tag facet bar ordered by tag name
  So that a tag keeps its place instead of reshuffling as scenario counts change

  # drv-030 bug 2: the facet bar used to sort most-used-first, so it reshuffled
  # run-to-run and a specific tag was hard to find. It must sort alphabetically.
  Scenario: The tag facet bar is alphabetical, not ordered by scenario count
    Given a captured BDD run whose scenarios carry tags in non-alphabetical count order
    When I view the rendered BDD report
    Then the tag facet bar is ordered alphabetically by tag name
