@drvctl-056 @manager-as-worker
Feature: The resume fast-path comes online, and the wake it answers
  drvctl-056 fixes a resume that was dead on arrival. The supervisor confirmed a
  resumed `claude` session "online" by waiting for its system/init frame BEFORE
  sending any user turn — but headless `claude --resume` emits nothing until it
  receives one, so every recorded-id bring-up timed out and silently cold-started.
  The fix primes the first driving turn (the resume nudge) before confirming
  online, so a genuinely resumable session actually resumes with its context
  intact. On an async wake (`draiver test --detach && draiver park`, drvctl-055),
  that same nudge now carries the reason the session was woken — the landed
  `test-result` — so the agent reads the verdict and claims review or iterates
  without re-deriving its own just-done state with a recall round-trip.

  The bring-up cascade's ordering and the context-headroom gate (a conversation
  parked near the window limit cold-starts with a clean window instead of resuming
  into an immediate auto-stop) are covered at the unit rung (internal/reconcile),
  mirroring drvctl-055's split: the reconciler-internal re-admit mechanics live
  there, while the agent-facing turn the resume actually delivers is pinned here
  over the real protocol engine.

  Rule: A resumed session is driven with a continuation nudge, not re-briefed

    Scenario: The resume turn continues the work rather than re-dumping the brief
      Given a resumed session being given its first driving turn
      Then the agent is nudged to continue its work, not cold-started from the brief

  Rule: An async wake tells the resumed agent to read the result it was woken for

    Scenario: The wake nudge names the landed test-result
      Given a session resumed because its detached test landed a result on its own log
      Then the driving turn tells the agent to read that result and hand off
