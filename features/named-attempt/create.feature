@drv-040 @named-attempt
Feature: Named attempts — attempt new --name <slug>
  drv-040 adds an optional --name to `draiver attempt new`: the new attempt's id
  IS the slug, so its address is <TICKET>@<slug>. This gives a codebase ledger's
  per-contributor perspective a provenance-bearing address (repo-facts@drv-040)
  instead of an opaque auto-number. A named attempt is first-class everywhere —
  the on-disk layout, the hash chain, the @-address form log/recall resolve, and
  audit — and the slug is validated so it can never break either the layout or
  the address parsing.

  Rule: a named attempt is a first-class attempt addressable by its slug

    Scenario: --name makes the attempt addressable as ticket@slug and it audits clean
      Given a ticket with no attempts yet
      Then an attempt named "repo-facts" is addressable as ticket@repo-facts and passes audit

  Rule: auto-numbering is unchanged when --name is omitted

    Scenario: an unnamed attempt beside a named one still auto-numbers
      Given a ticket with a named attempt "repo-facts"
      Then a subsequent unnamed attempt is still numbered 0001

  Rule: an unusable --name is refused before anything is written

    Scenario: a name colliding with an existing attempt is refused and writes nothing
      Given a ticket with a named attempt "repo-facts"
      Then re-using the name "repo-facts" is refused and leaves the existing attempt untouched

    Scenario: a name that would break the layout or the @-address is refused before any write
      Given a ticket with no attempts yet
      Then a name with a path separator, an at-sign, or whitespace is refused and writes nothing
