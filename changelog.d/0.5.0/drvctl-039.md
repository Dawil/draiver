---
section: Added
---
**The `Pending` control state — a runtime projection (drvctl-039).** A fifth
control state below Needs-me on the attention scale: an attempt that is
**enabled, has no live agent, and no open escalation** — a *self-resolving wait*
that advances on a gate opening or an activation firing, needing **zero human
attention** (Needs-me needs a human; Pending needs nothing). It renders as a
quiet count, like Running. Pending is a **read-time projection, never a log
event** — the hash chain stays clean (tier discipline). Crucially it is kept
**off `Attempt.State`**: the reconciler admits precisely the log-`Running`
attempts, so folding Pending into the stored state would hide desired-but-not-
yet-live attempts from admission. Instead `project.Control(state, enabled, live)`
(and the `Attempt.Control()` method over a new runtime `Live` bit) overlays
Pending onto Running only when enabled and no live agent; every other state
passes through untouched. Liveness is a `session.json` pid + signal-0 probe,
exposed as `session.Alive` — the CLI/project tier reads the OS itself rather than
asking the daemon, mirroring `reconcile.OSProc.Alive`. `draiver status` now
prints a **`Pending`** count at the quiet end of the summary line. `state.md`
regeneration is unchanged — it stays the pure log projection (`Running`). The
human-facing *reason* ("waiting on X") and board rendering land with drvweb-015 /
drvctl-040 once forward gates exist.
