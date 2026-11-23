Feature: Artefact check fixture

  Scenario: A scenario that shoots
    Given a seeded fixture
    Then the fixture page is shot

  Scenario: A scenario that only asserts
    Given a seeded fixture
    Then the fixture is asserted

  Scenario: A scenario that attaches text
    Given a seeded fixture
    Then the fixture output is attached

  Scenario: A scenario that shoots via a helper
    Given a seeded fixture
    And the fixture output is shot by helper
