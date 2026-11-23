@ui @drvweb-031
Feature: Web UI polish — cleared compose box, no Sync confirm, stable card text, no duplicate tab headings

  Four small, independent fixes to the HTMX board. Each scenario drives a real
  internal/web server in a live browser: an awaited in-page probe (internal/webshot
  `Probe`) performs the interaction and reports what the browser actually holds —
  field values, element geometry, panel headings — and the run's screenshot is the
  scenario's artefact.

  Rule: A successful submit empties its text box; a failed one keeps it

    Scenario: Note, gotcha and decision from the compose box each clear the text but keep the type and 📬 choice
      Given a Running attempt page for drvweb-031
      When I post a note, a gotcha and a tagged decision with 📬 unticked from the drvweb-031 compose box
      Then after each post the drvweb-031 compose body and tags are empty while the chosen type and the unticked 📬 stay
      And the three drvweb-031 entries are on the attempt's log

    Scenario: The Review "Decision — reopen to Running" button clears the compose box
      Given a Review attempt page for drvweb-031
      When I type a reason and press the drvweb-031 Review Decision button
      Then the drvweb-031 compose body is empty after the Review action
      And the drvweb-031 decision reason is on the log and the attempt is Running again

    Scenario: The Review "Done — close" button clears the compose box
      Given a Review attempt page for drvweb-031
      When I type a reason and press the drvweb-031 Review Done button, accepting its confirm
      Then the drvweb-031 compose body is empty after the Review action
      And the drvweb-031 done reason is on the log and the attempt is Done

    Scenario: Answering an escalation from the page clears the answer box
      Given a Running attempt page for drvweb-031 with an open escalation
      When I type an answer and press the drvweb-031 Resolve button
      Then the drvweb-031 escalation is resolved with my answer and no answer box still holds the text

    Scenario: A failed submit leaves the text in place
      Given a Running attempt page for drvweb-031
      When I post a drvweb-031 note that supersedes a seq that does not exist
      Then the drvweb-031 compose body still holds the rejected note and nothing was logged

  Rule: Sync (git pull) runs without a confirm dialog

    Scenario: Clicking Sync runs straight away with no confirm dialog
      Given a Running attempt whose base has advanced past the branch
      When I press Sync on the drvweb-031 Git controls tab without accepting any dialog
      Then the drvweb-031 Sync ran and reported success, and Push still confirms

    Scenario: A refused Sync on a dirty checkout still shows its failure banner
      Given a Running attempt whose checkout is dirty
      When I press Sync on the drvweb-031 Git controls tab without accepting any dialog
      Then the drvweb-031 Sync reported its dirty-checkout failure banner

  Rule: Dashboard cards reserve room for the corner buttons only on the top line

    Scenario: Card text below the button row uses the full width and never sits under a button
      Given a drvweb-031 board with cards carrying every corner-button combination
      When I measure the drvweb-031 cards at desktop width, at rest and while hovered, and at phone width
      Then on every drvweb-031 card the title spans the card's full width, no button overlaps any text, and hovering reflows nothing

  Rule: A tab panel does not repeat its tab's name as a heading

    Scenario: The Spec panel and its sibling panels carry no heading that repeats the tab label
      Given a drvweb-031 Review attempt with promotions and a captured BDD run
      When I open each drvweb-031 attempt tab in the browser
      Then no drvweb-031 tab panel starts with a heading repeating its tab label, and each panel is labelled by its tab
