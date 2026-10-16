@drvctl-053 @progressive-brief
Feature: A wants: ticket is told it is a manager
  brief.Build used to be byte-identical for every ticket and stripped spec
  frontmatter, so a wants: coordinator could not even see its children and
  defaulted to implementing its own spec. The brief now branches on wants:: a
  coordinator gets its roster, the coordinator protocol, and — as the brief's
  freshest tail — the spine-authored wake reason for this wake.

  Scenario: A coordinator's brief carries its team, the protocol, and the wake reason
    Given a coordinator attempt that wants two children and was woken by a child escalation
    Then the brief lists each child's control-state and open escalations
    And the brief injects the coordinator protocol
    And the wake reason is the tail of the brief

  Scenario: A ticket with no wants gets the worker brief unchanged
    Given a plain worker attempt with no wants edges
    Then the brief carries no manager injection
