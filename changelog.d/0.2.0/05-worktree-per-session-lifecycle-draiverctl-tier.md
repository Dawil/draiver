---
section: Added
---
**Worktree-per-session lifecycle (`draiverctl` Tier 0).** The isolation
primitive Admit/Retire depend on: each session gets its own git worktree, so
parallel sessions on one repo never collide and competing attempts stay
comparable (`internal/worktree`). A `Manager` exposes the three reconcile-loop
verbs — `Create` on spawn (idempotent and crash-safe; re-attaches to an
existing per-attempt branch so a stopped attempt can resume), `Remove` on
retire (checkout only by default; opt-in branch deletion), and `Reconcile` on
daemon restart, which sweeps worktrees left orphaned by a crash and any whose
attempt is no longer wanted. Checkouts live under a per-repo managed base
outside the repository (see drvctl-010) on a `draiver/<ticket>/<attempt>`
branch; the Manager holds no state, re-deriving the truth from `git worktree
list` each call (drvctl-003).
