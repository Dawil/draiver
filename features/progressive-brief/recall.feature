@drv-026 @progressive-brief
Feature: Supersede a stale fact and recall it on demand
  drv-026 replaces the brief's full-log dump with a deterministic fold over the
  immutable log. A fact is live until a later event supersedes it; superseding a
  stale fact (rather than appending a contradiction) drops it from the brief's
  index while its body and history stay one `recall` away — and the hash chain
  stays intact.

  Rule: Superseding retires a fact from the index without mutating the log

    Scenario: A superseded fact leaves the brief but stays recallable
      Given an attempt with a fact superseded by a later one
      Then the brief indexes the live fact and not the superseded one
      And recall of the superseded seq still returns its body
      And recall of the chain returns the history oldest-to-newest
      And audit still passes over the superseding chain

  Rule: Tags are an opaque retrieval axis draiver never interprets

    Scenario: A tag filters the index but carries no behaviour
      Given an attempt with two facts tagged differently
      Then recall filtered by a tag returns only the matching fact
