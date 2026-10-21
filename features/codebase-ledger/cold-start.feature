@drv-038 @codebase-ledger
Feature: An agent reads from and writes to its repo's codebase ledger
  drv-038 closes the consumption side of the codebase-ledger epic (drv-035). An
  agent working a code ticket discovers the repo's belief ledger at cold-start —
  the brief surfaces the type: codebase ticket(s) bound to the ticket's repo as a
  library pointer, resolved repo-derived — and contributes codebase-level findings
  back into it by opening its own scoped perspective (a fresh attempt) on the
  ledger. The invariant handbook teaches both halves, with a clear ticket-local vs
  codebase-level rule. A repo with no ledger bound is entirely unchanged.

  Rule: The cold-start brief surfaces the repo's bound ledger as a library pointer

    Scenario: A code ticket's brief points at its repo's codebase ledger, and only that one
      Given a code ticket whose repo has a bound codebase ledger and a decoy ledger on another repo
      Then the brief surfaces the bound ledger as a pointer and only that ledger

    Scenario: A repo with no ledger bound is unchanged
      Given a code ticket whose repo has no codebase ledger bound
      Then the brief carries no codebase-ledger section

  Rule: Findings are written into the agent's own scoped perspective on the ledger

    Scenario: A codebase-level finding is recorded on a scoped perspective attempt
      Given a codebase ledger and an agent's own scoped perspective attempt on it
      Then a codebase-level finding lands on that perspective, leaving the ledger's own log intact

  Rule: The handbook teaches the read and write protocol

    Scenario: The invariant protocol teaches consulting and contributing to the ledger
      Then the handbook teaches reading the ledger and the ticket-local vs codebase-level write rule
