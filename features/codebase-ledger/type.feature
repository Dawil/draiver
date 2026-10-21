@drv-037 @codebase-ledger
Feature: A ticket's type gates it as an ordinary ticket or a codebase ledger
  drv-037 adds a `type` to a ticket's spec: `ticket` (the default, an ordinary
  unit of work) or `codebase`, a belief *ledger* for a codebase. The type carries
  identity — a ledger has no setpoint and no actuation loop, so it is never
  enabled, its resting control state is Parked, and it never reaches Done; its
  only terminal state is Archived. A ledger is otherwise just a ticket: its log,
  recall, and board visibility are unchanged.

  Rule: An absent type leaves every existing ticket an ordinary ticket

    Scenario: A ticket authored with no type behaves exactly as today
      Given a ticket authored with no type field
      Then it loads as an ordinary ticket resting in Running

  Rule: A codebase ledger never actuates and rests Parked

    Scenario: A codebase ledger rests Parked and the reconciler never runs it
      Given a codebase ledger authored with type codebase
      Then the ledger rests in Parked and is not enabled
      And the reconciler never admits the ledger

    Scenario: A codebase ledger has no Done transition but can be Archived
      Given a codebase ledger with a stray done event and then an archive
      Then the ledger never reaches Done and rests Parked
      And the ledger is taken off the board by the archive

  Rule: A ledger surfaces to the human only through the normal state machine

    Scenario: An open escalation on a ledger surfaces Needs-me
      Given a codebase ledger with an open escalation
      Then the ledger surfaces as Needs-me

    Scenario: A resting ledger is absent from the Running and attention totals
      Given a codebase ledger at rest alongside an ordinary running ticket
      Then the rollup counts the ordinary ticket but not the resting ledger

  Rule: The type field is validated and otherwise changes nothing

    Scenario: An unknown ticket type is rejected
      Given a spec hand-edited with an unknown type
      Then loading the ticket is rejected with a clear message

    Scenario: recall and log operate on a ledger with no new flags
      Given a codebase ledger carrying a logged decision
      Then recall and log operate on the ledger unchanged
