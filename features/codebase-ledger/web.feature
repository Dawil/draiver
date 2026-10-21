@drv-039 @codebase-ledger
Feature: The board renders codebase ledgers apart from code tickets
  drv-039 is the web-UI half of the codebase-ledger epic (the model/CLI half is
  drv-037). A `type: codebase` ticket is a belief *ledger*, not a unit of work, so
  it must render differently and live in its own view: it is never enabled (no
  play/enable control), it is never Done (no Done affordance — its only terminal
  state is Archived), and it rests Parked. A filter toggle switches the board
  between code tickets and codebase ledgers; by default ledgers stay off the
  code-ticket board, surfacing into it only when they hit an attention state — an
  open escalation (Needs-me). Code tickets render and behave exactly as today.

  Rule: A codebase ledger renders with no play/enable control and no Done affordance

    Scenario: A ledger card carries no play control, no Done affordance, and a Parked rest
      Given a board with a resting codebase ledger and an ordinary ticket
      Then the ledger card carries no play control and no Done affordance
      And the ledger card is marked a ledger resting Parked

  Rule: The operator toggles between code tickets and codebase ledgers

    Scenario: Ledgers are absent from the code view and present in the ledgers view
      Given a board with a resting codebase ledger and an ordinary ticket
      Then the code view shows the ordinary ticket but not the resting ledger
      And the ledgers view shows the ledger but not the ordinary ticket

  Rule: A ledger with an open escalation still surfaces for the operator's attention

    Scenario: An open-escalation ledger surfaces into the code view through the Stuck path
      Given a board with a codebase ledger that has an open escalation
      Then the code view surfaces the ledger in the attention path marked a ledger
      And the ledgers toggle shows the ledger needs attention

  Rule: Code tickets render and behave exactly as today

    Scenario: An ordinary ticket keeps its play control on the code view
      Given a board with a resting codebase ledger and an ordinary ticket
      Then the ordinary ticket on the code view still shows its play control
