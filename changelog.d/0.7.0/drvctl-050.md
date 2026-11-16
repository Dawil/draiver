---
section: Added
---
**Downward resolution over `wants:` with `resolve --by`, and move-up escalation
under pre-digest (drvctl-050).** Before this change, a coordinator added to the
human's workload instead of absorbing it. A child escalation landed in Needs-me
*and* woke the pre-digest parent, which escalated as well, so one problem made
two board items. Resolving the parent also left the child blocked. The `wants:`
edge now carries a third flow, **resolution, downward**. The new verb
**`draiver resolve <CHILD> <seq> --by <PARENT>`** (`repo.ResolveBy`) lets a
coordinator answer one of its child's escalations. It is authorised by the edge
(PARENT must `wants:` CHILD), uses the same seq/open-escalation validation as
plain `resolve`, and records a distinct `parent:<PARENT>` actor that shows up in
`brief`/`log`. It only ever writes a `resolution`, so a parent can unblock a
child but can never move it from Review to Done. If the parent is not
authorised, the command fails and writes nothing. Escalation now **moves** up
instead of being **copied**. Under pre-digest, a Needs-me child with an
enabled, Running pre-digest wanter is shown as **Pending "escalated to `<P>`"**,
credited to the parent and left out of the Needs-me count and `draiver inbox`.
The mapping comes from `project.DeriveEscalated`, which uses the same predicate
as the reconciler's wake path, plus `Attempt.EscalatedTo` /
`Attempt.BoardState()`. If several tickets want the child, the lexically first
one wins. Under passthrough the child stays Needs-me. This is purely read-time:
`draiver status`, the web board and `inbox` change, but the child's log, its
derived state and the reconciler's admission/wake behaviour do not.
