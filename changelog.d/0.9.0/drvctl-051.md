---
section: Added
---
**Generalised manager activation — child lifecycle doorbells, the `woken-by`
event, and serial queue/replay (drvctl-051).** Reverse-`wants:` activation in
`internal/reconcile/reverse_wants.go` (drvctl-041's escalation-only, level-
triggered wake) is rewritten into the manager-as-worker wake from
`docs/manager-as-worker.md`. A wanted child's **escalation, review, or done**
event (the `doorbellTypes` trigger set) rings an **edge-triggered doorbell** — a
transition, so a child merely *sitting* in Review never re-wakes its manager. When
the spine wakes a pre-digest, enabled manager it first appends a durable
**`woken-by` event** to the *manager's own* log (body `woken by <child> #<seq>`,
plus new omitempty `woken_child`/`woken_seq` fields on `event.Event` that enter
the hash only when set, so existing events hash as before). That one entry serves
as dedup record, replay ordering, and the newest line of the manager's cold-start
`brief`. The **queue** is derived — doorbells with no matching `woken-by` — and is
drained **oldest-first, one doorbell per wake**: a manager with an activation and
no later `park` stays desired while it handles it; once it `park`s it is reaped
to Pending and re-admitted for the next doorbell. A manager with its **own open
escalation is never woken**; its doorbells accumulate and replay resumes once the
human resolves it. Behaviour change: a woken coordinator now reconverges to
Pending on its own `park`, not when the child's escalation resolves (drvctl-044's
test migrated). The staleness watchdog and StartLimit loop-safety were deferred
to follow-on tickets.
