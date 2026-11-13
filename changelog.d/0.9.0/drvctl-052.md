---
section: Added
---
**`draiver park` — suspend an attempt to Pending without escalating
(drvctl-052).** A third legitimate way for an agent to end its turn, alongside
`review` and `escalate`: **`draiver park TICKET [REASON]`** appends a durable
`park` turn-end and exits 0, leaving the attempt **Pending** (enabled, no live
agent, self-resolving) with nothing on the human's inbox. `park` is a
non-lifecycle event, so the log still derives `Running` and the existing
`Control` fold projects it to Pending once the session is reaped; the log and
attempt persist and a later wake resumes from `brief`. It **refuses** (nonzero,
nothing appended) when the attempt has no pending wake source, since it would
otherwise sleep forever. Wake sources sit behind a new abstract seam,
`internal/wake` (`wake.Pending`); the only source today is the coordinator one —
a `wants:` ticket with at least one non-Done child — with async jobs left for
drvctl-055. The completion gate (`internal/completion`) now folds the wake check
in: a `park` counts as a hand-off only when the attempt is parkable, and an
unmet success turn on a parkable attempt gets a new **`Parked`** outcome instead
of an escalation — fixing the case where a `wants:` coordinator that delegated
via `resolve --by` (which writes to the child's log) and stopped was escalated
onto its own ticket. Non-parkable leaf behaviour (nudge, then escalate) is
unchanged; the existing coordinator gate and reverse-`wants:` withdrawal retire
the parked attempt to Pending, and `internal/watch` promotes `draiver park` into
the supervised log like `review`/`escalate`. Design:
`docs/manager-as-worker.md`.
