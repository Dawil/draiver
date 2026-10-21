@drv-032 @progressive-brief
Feature: Recall enumerates tags and is attempt-aware
  drv-032 closes three recall gaps surfaced while inspecting a multi-attempt
  ticket: there was no way to list the tags in use, no way to address a sibling
  attempt with the `ticket@attempt` form the rest of the CLI accepts, and no cue
  that sibling attempts even exist. recall now lists the distinct tags of the
  in-scope facts, resolves the `@attempt` address form, and names the resolved
  `ticket@attempt` — flagging the siblings when the attempt was not chosen.

  Rule: --tags enumerates the distinct tags of the in-scope facts

    Scenario: recall lists the tags in use, sorted, with counts
      Given an attempt with two facts tagged differently
      Then recall --tags lists the distinct tags sorted alphabetically with counts

  Rule: recall is attempt-aware — it addresses and names a specific attempt

    Scenario: recall addresses a sibling attempt and flags the others
      Given a ticket with two attempts each carrying a distinct fact
      Then recall of the ticket@attempt address targets that attempt independently
      And recall of the ticket alone resolves to the latest attempt and flags its siblings
