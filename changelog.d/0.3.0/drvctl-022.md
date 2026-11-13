---
section: Fixed
---
**Resumed sessions get a driving turn again, and a session can no longer stop
at `result: success` without handing off (`draiverctl`, drvctl-022).** drvctl-016's
brief-on-reset coupling gated the sole driving prompt behind `if w.spawned`, so a
resumed headless `stream-json` session — which produces nothing until it receives a
user turn — came online, restored its context, and idled forever, never continuing
the work and never filing a `review` or `escalation`. From the supervisor's stream
the attempt "got to `result: success` and stopped" with nothing filed, stranding it
in Running + enabled; this broke every resume path, including the core
`escalate → resolve → resume` arc. **Part A** restores drive keyed on session
identity: `admit` branches on `wired.spawned` — a fresh spawn (empty context) still
gets the full cold-start brief, while a resume (recorded id came back online, context
intact) gets a new short `protocol.InjectResumeNudge` that points to `draiver brief
<ticket>` and restates the log/hand-off obligation without re-dumping spec + log.
**Part B** adds defense in depth: a new `internal/completion.Gate`, constructed in
`bringUp` and threaded through `ingest`/`dispatch`, reacts to `agent.EventTurnEnd`
with `Turn == "success"`; using the same log-tail-vs-baseline check the protocol gate
uses, it looks for a `review`/`escalation` newer than the session baseline, injects a
one-shot nudge if the obligation is unmet, and escalates-and-halts to a human on a
later still-unmet success turn — so a stalled "done" lands on the board instead of
idling. The gate latches so it cannot loop.
