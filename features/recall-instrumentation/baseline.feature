@drv-036 @recall-instrumentation
Feature: Measure belief utilisation by counting recalls
  The codebase-ledger epic rests on one unproven claim: that accumulated belief
  actually gets read by later agents. drv-036 adds cheap, offline instrumentation to
  `draiver recall` — one line per invocation naming the ticket/attempt read and the
  seq(s) surfaced — so the before/after of ledgers is visible. It measures
  consumption, and ranks which facts carry weight, without touching what the agent
  sees.

  Rule: Every recall is recorded, foldable per-ticket and per-fact

    Scenario: Recalls accumulate into per-ticket and per-fact counts
      Given an attempt whose facts are recalled a few times
      Then each recall is recorded with the ticket and the facts it surfaced
      And the records fold into per-ticket and per-fact recall counts

  Rule: The instrumentation is an offline, pure-read side-channel

    Scenario: Recording never changes what recall surfaces, and stays offline
      Given an attempt whose facts are recalled a few times
      Then recording is best-effort and never changes what recall surfaces
      And the counts are retrievable offline from the on-disk sink
