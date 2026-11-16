---
section: Added
---
**Reverse-`wants:` activation — a child's escalation wakes its Pending
coordinator (drvctl-041).** The systemd `OnFailure=` analog: enable flows
*down* a `wants:` edge (drvctl-038), and now escalation flows *up* the same
edge. A new additive contributor, `activateReverseWants`
(`internal/reconcile/reverse_wants.go`), runs last in the reconciler's
`desired()`: when a ticket X has an open escalation, every enabled ticket W whose
`spec.md` `wants:` X (found via drvctl-037's `project.ReverseWants`) is made
desired, so a dormant coordinator is woken to assess. Running after the forward
gate means the wake overrides the coordinator's own shut `after:`/`requires:`
gate — a child crying for help is exactly when it should run. It is narrower than
forward propagation: it never mints an attempt (nothing to wake if the
coordinator has none) and it respects a human `disable`. Children never name
their coordinator. Union-only, so it cannot regress the direct/wants/gate passes.
The spec's StartLimit loop guard is **not** implemented — the StartLimit ceiling
does not exist yet, so that acceptance test is skipped.
