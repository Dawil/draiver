---
section: Added
---
**Coordinator supervision dial — `passthrough` and `pre-digest` (drvctl-042).**
A per-ticket supervision mode now gates drvctl-041's reverse-`wants:` activation:
on a sub-ticket escalation, the mode of the Capability that `wants:` it decides
whether its coordinator wakes. **`passthrough`** (the floor and built-in default)
routes the escalation straight to Needs-me and spawns no coordinator;
**`pre-digest`** wakes the coordinator to assess across its children and post one
consolidated escalate-and-recommend for a human to ratify. A new verb **`draiver
ctl supervision <ticket[@attempt]> <passthrough|pre-digest>`** records the mode as
a `supervision` log event (mode on `Outcome`, last event wins, replayed by
`brief`) — a mutable control setting like `enable`/`disable`. The global/project
default comes from the new config key **`default_supervision`**; a bad value fails
daemon start. Auto-execute is deferred and refused by name. The model lives in
`internal/project/supervision.go` (`ParseSupervision`, `DeriveSupervision`,
`Effective`); the reconciler's `wakeWanter` only wakes a coordinator whose
effective mode is `pre-digest`. A coordinator never writes into a child's log.
