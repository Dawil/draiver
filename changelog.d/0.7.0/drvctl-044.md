---
section: Fixed
---
**Enabling a `wants:`-coordinator holds it Pending instead of admitting it
(drvctl-044).** When you enabled a Capability coordinator (a ticket with
`wants:` edges), it was admitted as a live agent straight away and started
working the whole epic. The design says it should enable its children and wait
as a dormant Pending shell. `desired()`'s direct-enable pass admitted every
enabled Running attempt unconditionally. The forward gate only trims attempts
with unmet `after:`/`requires:` predecessors, and reverse-wants only wakes a
coordinator that is already Pending, so neither one ever parked a coordinator.
A new **coordinator dormancy gate** (`applyCoordinatorGate`,
`internal/reconcile/coordinator_gate.go`) runs after `propagateWants` (so the
children are still pulled into the fleet) and before `activateReverseWants`. It
removes any desired attempt whose ticket has `wants:` edges from admission
unless **all** of its wanted children are Done, which is when the coordinator is
admitted for its own final review claim. Coordinators that are enabled through a
parent are held the same way. Unlike the forward gate, this gate is **not
fire-once**. A coordinator that the old code had already admitted is retired
back to Pending on the next reconcile with no manual disable/re-enable, and a
woken pre-digest coordinator parks again once its child's escalation resolves.
Reverse-wants is unchanged: a child escalation still wakes a pre-digest
coordinator, while a passthrough coordinator stays dormant until all of its
children are Done.
