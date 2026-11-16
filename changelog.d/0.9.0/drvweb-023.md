---
section: Added
---
**Manager signal on the board — coordinator cards show their wake reason and
queue depth (drvweb-023).** A running manager used to look like any running
ticket, and its Pending→Running→Pending oscillation was invisible. Now a ticket
carrying `wants:` edges renders as a distinct **coordinator card**
(`card-coordinator`, a green border) whose line surfaces the control plane's own
recorded reason: "manager — on it, re: `C1`" from its latest `woken-by`
activation event (drvctl-051), plus "· N queued" for child doorbells that have
no activation yet, or "manager — idle" when there is no active wake. The signal
is derived in the `project` package (`DeriveCoordinatorSignal`,
`internal/project/coordinator.go`) rather than by importing the reconciler. The
Capability panel on the detail page (drvweb-017) gains the same line for the
manager's **own** current action, not just its children's states. Also fixes a
board bug where a Pending card's `WaitingReason` such as "escalated to `P`"
showed literal backticks: it (and the Capability panel's gate reasons) now
renders through the `inlineMD` markdown pipeline. Acceptance coverage in
`features/manager-signal/board.feature`; a follow-up commit points its
screenshots at the full board page `/` rather than the unstyled `/board`
partial.
