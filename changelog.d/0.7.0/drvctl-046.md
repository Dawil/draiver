---
section: Fixed
---
**A drifted or clashing worktree self-heals or escalates instead of wedging
admit forever (drvctl-046).** `internal/worktree` identified a managed worktree
two ways — by **branch** (`List`/`find`/`Reconcile`) and by **path**
(`Create`/`Remove`/`Dirty`/`pathFor`) — and nothing enforced that they agreed,
so a mismatch failed the same way every reconcile tick as a `worktree-clash`
with no recovery. Four fixes, sharing one set of helpers (`branchAt`,
`listRecords`, `worktreeOnBranch`, `keyForPath`, `removeAtPath`): **(B) a
managed path drifted onto another branch or a detached HEAD** is now reclaimed
by `Create` (`worktree remove --force` + `prune`, then re-added on the attempt's
branch) — force-reclaim was chosen over refuse-if-dirty because the path is
draiver's by construction; **(A) the attempt's branch checked out at a foreign
path** (typically the origin repo) makes `Create` return an `ErrCreate` naming
the offending worktree, still on the transient `worktree-clash` health path —
draiver never steals a human's checkout; **`Reconcile` no longer aborts** on a
zero-key (foreign/detached) entry under the managed base — it sweeps such strays
by path and returns them, and the reconciler's `Adopt` raises one idempotent
escalation per stray, flipping the attempt to Needs-me (mirroring
`escalateNoRepo`; the escalation drvctl-027 deferred); and **`Dirty` is
branch-aware** — a path not on the attempt's branch reports dirty, so the
retire guard preserves it rather than force-removing a checkout whose real state
it never read.
