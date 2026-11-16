---
section: Added
---
**Per-attempt daemon health log at `session/ctl.jsonl`, surfaced in `ctl logs`
(drvctl-027).** When the supervisor cannot run an enabled attempt the failure
used to be invisible — `admit` failed every tick with the operational `Logf`
defaulting to a no-op and `ctl logs` showing only the agent stream, so a wedged
attempt (its branch checked out in another worktree, or a corrupt/empty object
at the branch tip) looked idle. Each attempt now gets an append-only,
rebuildable `session/ctl.jsonl` (new `Root.SessionCtlLogPath`) recording
**edge-triggered** daemon health transitions — an `error-start` when an error
class begins affecting the attempt and an `error-end` when it clears, not one
line per failed tick. Edges are diffed against an in-memory per-attempt
active-class set beside the run table (rebuildable — a restart re-observes and
re-emits), and an end-of-tick sweep closes classes for attempts that leave the
desired set so the board's red dot resolves instead of stranding on a lone
start. The worktree-clash class is recognized by cause — a new
`worktree.ErrCreate` sentinel wrapping every `git worktree add` failure — rather
than by string-matching git stderr, so it spans *any* cut failure (branch
checked out elsewhere, corrupt object at the tip, missing base ref, occupied
path); everything else falls to an `admit-failed` catch-all. Class strings are a
stable contract with the board's red error dot (drvweb-008); `no-network` /
`model-unreachable` stay reserved for a future stream-health seam. `ctl logs`
interleaves the health lines with the agent stream in the human render (error
role for `start`, system role for `end`, each with its own recorded timestamp)
and `-f` follows both files; the daemon's free-text `Logf` now routes to stderr
(was a no-op) and `--json` passthrough of `stream.jsonl` is unchanged.
