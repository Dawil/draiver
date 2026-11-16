@changelog @drv-052
Feature: Per-ticket changelog fragments, a generated CHANGELOG.md, and a required changelog rung
  As a maintainer
  I want every ticket to carry its own changelog fragment, checked by a required test rung
  So that the changelog cannot drift and concurrent tickets never conflict on CHANGELOG.md

  # Each ticket writes changelog.d/<ticket-id>.md (format: changelog.d/README.md);
  # CHANGELOG.md is rendered from the fragments by `go run ./cmd/changelog generate`.
  # The rung scenarios drive the REAL `draiver test` binary in a scratch git repo on a
  # draiver/drv-900/0001 attempt branch whose pyramid declares a `changelog` rung running
  # the real checker — so they also prove draiver exports $DRAIVER_TICKET to the rung.
  # Not a UI feature: no screenshots; the rung/generator output is attached as text.

  Rule: The changelog rung requires a valid fragment for the attempt's ticket

    Scenario: The rung fails, naming the expected path, when the ticket's fragment is missing
      Given a scratch attempt repo for drv-900 with a changelog rung
      When I run draiver test on the changelog rung
      Then the changelog rung fails
      And the rung output names "changelog.d/drv-900.md"

    Scenario: The rung passes with a valid fragment
      Given a scratch attempt repo for drv-900 with a changelog rung
      And the drv-900 fragment is a valid "Added" entry
      When I run draiver test on the changelog rung
      Then the changelog rung passes
      And the rung output names "ticket drv-900 from $DRAIVER_TICKET"

    Scenario: The rung passes with a valid skip fragment
      Given a scratch attempt repo for drv-900 with a changelog rung
      And the drv-900 fragment is a skip with reason "test-only refactor"
      When I run draiver test on the changelog rung
      Then the changelog rung passes
      And the rung output names "skip: test-only refactor"

    Scenario: The rung fails on a fragment with an unknown section
      Given a scratch attempt repo for drv-900 with a changelog rung
      And the drv-900 fragment has an unknown section
      When I run draiver test on the changelog rung
      Then the changelog rung fails
      And the rung output names "unknown section"

    Scenario: The rung fails on a fragment with an empty body
      Given a scratch attempt repo for drv-900 with a changelog rung
      And the drv-900 fragment has an empty body
      When I run draiver test on the changelog rung
      Then the changelog rung fails
      And the rung output names "empty body"

    Scenario: The rung fails on a skip fragment with an empty reason
      Given a scratch attempt repo for drv-900 with a changelog rung
      And the drv-900 fragment is a skip with an empty reason
      When I run draiver test on the changelog rung
      Then the changelog rung fails
      And the rung output names "non-empty reason"

    Scenario: The rung fails when another ticket's fragment is malformed
      Given a scratch attempt repo for drv-900 with a changelog rung
      And the drv-900 fragment is a valid "Added" entry
      And another ticket's fragment has an unknown section
      When I run draiver test on the changelog rung
      Then the changelog rung fails
      And the rung output names "changelog.d/drv-901.md"

    Scenario: Run by hand, the check falls back to the attempt branch for the ticket id
      Given a scratch attempt repo for drv-900 with a changelog rung
      And the drv-900 fragment is a valid "Added" entry
      When I run the changelog check by hand
      Then the changelog rung passes
      And the rung output names "ticket drv-900 from branch draiver/drv-900/0001"

    Scenario: Run by hand off an attempt branch with no DRAIVER_TICKET, the check fails rather than passing silently
      Given a scratch attempt repo for drv-900 with a changelog rung
      And the drv-900 fragment is a valid "Added" entry
      And the scratch repo is on its main branch
      When I run the changelog check by hand
      Then the changelog rung fails
      And the rung output names "cannot determine the ticket id"

  Rule: The generator renders fragments deterministically

    Scenario: Fragments render into Keep-a-Changelog sections in the documented order, omitting skips
      Given a scratch fragment tree spanning several sections, prefixes and a skip
      When I generate CHANGELOG.md
      Then the sections appear in Keep-a-Changelog order
      And entries within a section are ordered by ticket prefix then number
      And the skip fragment renders nothing

    Scenario: Regenerating is idempotent
      Given a scratch fragment tree spanning several sections, prefixes and a skip
      When I generate CHANGELOG.md
      And I generate CHANGELOG.md again
      Then the two renders are byte-identical
      And the generator's staleness check reports CHANGELOG.md current

  Rule: The migrated CHANGELOG.md is reproduced from fragments alone

    # Ticket branches only add fragments; CHANGELOG.md is regenerated on demand (at
    # release). So the committed file may lag fragments added since — but it must be
    # EXACTLY the render of the fragments it covers: every released section, plus the
    # [Unreleased] entries it carries. A hand edit to CHANGELOG.md breaks this.
    Scenario: Regenerating CHANGELOG.md from fragments reproduces the committed file
      Given the repository's changelog.d fragments and its committed CHANGELOG.md
      When I render CHANGELOG.md from the fragments it covers
      Then the render equals the committed CHANGELOG.md byte-for-byte
