---
section: Added
---
**Landing is now a `ctl` primitive — `ctl merge` / `ctl sync` on a per-attempt
base branch (drvctl-021).** Every attempt records a `base:` branch (default: the
bound repo's current branch at create, overridable with `--base` on `draiver
new` / `draiver attempt new`), and the worktree is cut from it so a fresh land is
a clean fast-forward. `ctl merge <ticket>` lands the attempt's branch into its
base **fast-forward only** — gated to a stopped Review attempt with a clean
checkout, keeping the branch until the land is durable, and recording `done` on
success so Done comes to mean *the code is in the target branch*. `ctl sync
<ticket>` back-merges the base into the branch additively (one merge commit, no
SHA rewrite — no rebase anywhere), so a resume continues on the updated tip and a
diverged branch becomes ff-landable; a diverged `merge` refuses and points to
`sync`, or `merge --sync` does sync-then-ff in one shot. `ctl merge --dry-run`
reports mergeability (via `--is-ancestor` + `merge-tree`) without mutating
anything. On failure the orthogonal `--escalate` / `--no-escalate` axis picks the
disposition, defaulting by actor kind: an `agent:*` land raises a durable
escalation (→ Needs me, halt), a `human:*` land exits nonzero with a message.
Conflict / divergence / dirty always abort cleanly and never force.
