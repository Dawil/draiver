---
section: Added
---
**Forward success gate — `after:`/`requires:` hold admission until a
predecessor succeeds (drvctl-040).** The first real scheduler increment: the
reconciler's `desired()` gains a final **gate** pass that holds a desired
attempt *Pending* (out of admission) until its ordering predecessors succeed.
`after: [X]` admits once **X reaches Review** (a success claim); `requires: [X]`
admits once **X reaches Done** (the merged terminal). A predecessor's threshold
is the **best state across all its attempts** — success is monotonic, so a later
re-run of a predecessor never retracts a threshold an earlier attempt crossed —
and *every* named predecessor must clear for a multi-edge gate to open. The gate
is **edge-triggered and fire-once**: it holds only a *never-admitted* dependent
out; once a dependent is admitted (a session id is on record), a later change in
a predecessor — a Review flickering back to Running — never re-closes the gate,
so an already-running dependent is never reaped or restarted (staleness surfaces
at the integration test, not here). The latch is the durable **session.json**,
so it survives a `ctld` restart — an already-admitted dependent resumes rather
than re-gating. The gate runs *after* `wants:` propagation, so enable still flows
down from a gate-held (Pending) parent; the gate trims *admission*, not
desired-ness. No new log event and no on-disk scheduler state — the gate is a
derived computation re-run every tick.
