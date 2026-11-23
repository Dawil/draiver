@drv-061
Feature: Agents are steered off low-value reads
  As a maintainer paying for agent context
  I want the repo's config, docs and ignore files to keep big, unhelpful bytes out of reads
  So that agents spend context on code, not on prose, generated files or minified assets

  # These scenarios read the REAL repo (the checked-in files at HEAD), so they cannot
  # drift from what ships. Not a UI feature: no screenshots.

  Scenario: Slimming .test-pyramid.yaml did not change its config
    Given the repository's .test-pyramid.yaml
    Then it decodes to the pinned golden semantics
    And it parses to the same pyramid as the golden
    And it is under 120 lines and points at docs/test-pyramid.md for the prose

  Scenario: Search tools skip minified assets and generated BDD output
    Given the repository's ignore files
    Then .rgignore and .ignore both exclude "internal/web/static/*.min.js"
    And .rgignore and .ignore both exclude "features/_artifacts/"
    And a ripgrep search for "confirm(" matches no minified asset

  Scenario: Agent guidance names the cheap way to read
    Given the repository's agent guidance
    Then AGENTS.md says not to read the generated CHANGELOG.md
    And AGENTS.md says to grep then Read with offset/limit for large files
    And changelog.d/README.md says the version comes from version.FromChangelog
