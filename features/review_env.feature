Feature: Launch a review environment from the attempt page
  As a reviewer on the board
  I want to stand up a real, running instance of the feature under review
  So that I judge it by clicking around the working thing, not by reading the diff

  Scenario: The panel is offered on a Review attempt with a review-ready environment
    Given a Review attempt whose repo configures a review-ready environment
    When I open the attempt page
    Then the review-environment panel offers to launch it

  Scenario: Launching stands up a running instance and reveals its URL
    Given a Review attempt whose repo configures a review-ready environment
    When I launch the review environment
    Then the environment comes up and the attempt page reveals a clickable URL

  Scenario: Tearing down confirms the environment is gone
    Given a running review environment
    When I tear the review environment down
    Then the confirm-down healthcheck is red and the panel returns to rest

  Scenario: A teardown that leaves the service answering is flagged as a leak
    Given a running review environment whose teardown does not stop it
    When I tear the review environment down
    Then the panel warns of a leak rather than reporting the environment gone

  Scenario: A misconfigured review environment fails loudly instead of vanishing
    Given a Review attempt whose reviewEnvironment names an incomplete environment
    When I open the attempt page
    Then the panel shows a misconfiguration note and offers no launch

  Scenario: The launch form lets the reviewer set a custom ENV_NAME
    Given a Review attempt whose review environment declares a settable ENV_NAME
    When I open the attempt page
    Then the launch form offers an editable ENV_NAME input

  Scenario: A custom ENV_NAME parameterises the running instance
    Given a Review attempt whose review environment declares a settable ENV_NAME
    When I launch the review environment with ENV_NAME set to a custom value
    Then the running instance is parameterised by the chosen ENV_NAME
