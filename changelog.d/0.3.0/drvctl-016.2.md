---
section: Changed
---
**`restart` is now flush-to-depth + that same climb, with the reset depth named
explicitly (`draiverctl`, drvctl-016).** A single ordinal flush runs before the
cascade climbs back, so all points on the axis share one path: `--new-session`
(clear the session id → respawn fresh on the same worktree, cold-started),
`--new-worktree` (also rebuild the checkout from HEAD, discarding uncommitted
work by design), and `--new-attempt` (fork a child attempt via
`attempt.Create{From}`, inheriting tool/model/repo, the parent log preserved
immutably with a fork note). Deepest flag wins; every destructive reset records a
durable note so it is never silent. The old "context refresh" framing is dropped.
