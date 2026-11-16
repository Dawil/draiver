@changelog @drv-051
Feature: Every merged ticket has exactly one CHANGELOG entry
  As a maintainer reading the release notes
  I want every ticket that landed on the trunk to have its own CHANGELOG.md entry
  So that the changelog is a complete record and can be split into per-ticket fragments

  # drv-051 backfilled CHANGELOG.md for every merged ticket it was missing. The invariant
  # this guards: every ticket id that prefixes a commit subject in the history
  # (`git log --format=%s`, ids matching ^drv(ctl|web)?-N) is the id of EXACTLY ONE entry.
  # An entry is a top-level `- **…**` bullet; its id is the ticket id closing its bold
  # summary — `- **Summary (drvweb-027).**`. A mere mention in another entry's prose does
  # not count. drv-052 split the changelog into per-ticket fragments (changelog.d/) on
  # exactly this shape, so the shape is checked too. Since drv-052 these invariants hold
  # over the changelog AS RENDERED FROM changelog.d/ (ticket branches add a fragment;
  # CHANGELOG.md is regenerated at release), and a `skip:` fragment accounts for its
  # ticket without an entry. Not a UI feature: no screenshots.

  Scenario: Every ticket id in the git history is the id of exactly one CHANGELOG entry
    Given the repository's git history and its CHANGELOG.md
    When I collect the ticket ids prefixing commit subjects and the ids of CHANGELOG entries
    Then every commit-prefix ticket id is the id of exactly one CHANGELOG entry

  Scenario: Ticket entries follow the house shape under a Keep-a-Changelog heading
    Given the repository's git history and its CHANGELOG.md
    When I collect the ticket ids prefixing commit subjects and the ids of CHANGELOG entries
    Then every ticket entry sits under a Keep-a-Changelog heading inside a release section
    And no ticket entry's bold summary names a second ticket id

  # #7/#12 on drv-051: the backfill was cut into real semver releases, and `draiver --version`
  # is single-sourced from the changelog (drv-006) — so the binary must report the topmost
  # released `## [x.y.z]` section, never `[Unreleased]`.
  Scenario: draiver --version reports the newest released CHANGELOG section
    Given the repository's git history and its CHANGELOG.md
    When I build draiver and ask it for --version
    Then it reports the version of the topmost released CHANGELOG section
