@drvweb-024 @attempt-page
Feature: The board renders GFM pipe tables in event bodies
  Event and spec bodies are rendered server-side with goldmark. GFM pipe tables are
  not CommonMark core, so the single goldmark instance must register the table
  extension; otherwise a pipe table renders as its literal "|"/"---" source text.
  The scheme-floor protection from linkPolicy must survive the extension: a
  javascript: link inside a table cell is still blanked at render time.

  Scenario: A pipe table in an event body renders as an HTML table
    Given a board attempt with a markdown pipe table in an event body
    Then the attempt page renders the pipe table as an HTML table
    And the attempt page keeps the scheme floor inside the table
