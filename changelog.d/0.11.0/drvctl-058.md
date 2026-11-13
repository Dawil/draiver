---
section: Fixed
---
**A resumed agent is told what was logged while it was parked, so a
decision-reopen is no longer silently ignored (drvctl-058).** When a human adds a
`decision` to a Review attempt (or resolves an escalation), the attempt flips
back to Running and the reconciler resumes the kept-warm session — but that
session's restored context ends at its own hand-off, and the plain continuation
nudge gave no hint the log had changed, so the agent re-claimed review with stale
context without ever reading the decision. This regressed in drvctl-056: before
resume worked, every bring-up fell through to a cold-start brief whose "Current
understanding" index surfaced the decision by accident. `asyncWakeNote` in
`internal/reconcile/bringup.go` (which only handled detached test-result wakes)
is generalised into **`resumeWakeNote`**: it watermarks on the attempt's last
**session-authored** event (`agent:` actor, excluding `test-result` and
`woken-by`, which carry an agent actor but are external input), then names every
authoritative event above it — `decision`, `resolution`, `note`, `gotcha`,
`test-result` — by seq and headline, and tells the agent to `draiver recall` (or
`brief`) and act on them before handing off. The detached-test wake is now the
single-`test-result` case of the same note, keeping drvctl-056's wording (read
the verdict, hand off, no recall round-trip); a pure continuation resume with
nothing new still gets the plain nudge. Covered by
`TestDecisionReopenResumeNamesTheDecision` and a new decision-reopen scenario in
`features/manager-as-worker/resume-on-wake.feature`.
