---
section: Added
---
**`draiver review` refuses a claim below the test-pyramid target rung at HEAD
(drvctl-049).** Closes the loop for the test-pyramid epic (drv-009): the review
claim now enforces, by process control, that the target (top) rung was actually
climbed — the same seam as the permission gate and `merge`'s clean-checkout gate.
Before appending, `cmd/review.go` resolves the *attempt's* worktree (from
`attempt.md`'s repo via a new read-only `worktree.Manager.Locate`, not the cwd, so
review can still be run from outside a worktree), loads its
`.test-pyramid.yaml`, and folds the attempt's `test-result` events with the new
`pyramid.HighestAtHEAD`: the highest logged rung whose `commit` equals the
current HEAD. If that is below the target, the claim is refused with a message
naming the gap and the fix — climb it with `draiver test --log`, then claim
review. A result logged before a later commit is **stale** and does not count, so
committing after logging correctly invalidates the claim. The gate is additive
and fails open: no recorded repo, a non-git base, no live checkout, or no
`.test-pyramid.yaml` all skip it and review proceeds unchanged; only a genuine git
fault on a real base surfaces as an error.
