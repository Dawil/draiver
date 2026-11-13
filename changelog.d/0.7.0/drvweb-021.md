---
section: Added
---
**Test-pyramid badge on the board card and attempt header — the cleared rung at
HEAD (drvweb-021).** The UI half of the test-pyramid epic (drv-009): a logged
`test-result` was otherwise invisible on the board. Board cards (beside the
session dot) and the attempt-detail `<h1>` now render a compact pill such as
`unit ✓ · integration ✓` — every rung up to the highest one logged at the
attempt's current HEAD, folded with drvctl-049's `pyramid.HighestAtHEAD`.
Distinct states: **green** (result at HEAD, clean tree); **stale** (results
exist but at an older commit — dimmed, dashed, showing the recorded short SHA,
`data-stale`); **dirty** (a result names HEAD but the worktree has uncommitted
changes — dimmed, flagged `uncommitted`, `data-dirty`, so green means re-running
`draiver test` would still pass); and **none** (no `.test-pyramid.yaml` or no
result — nothing rendered). It is a read-only projection with no write path and
no stored SHA: HEAD and cleanliness are read live from the attempt's worktree
(`worktree.HeadSHA`, `worktree.DirtyAt`), falling back — when the daemon has
reclaimed the checkout on retire into Review — to the branch tip and committed
pyramid file via new `worktree.BranchSHA`/`FileAtBranch` and `pyramid.Parse`, so
the badge shows in exactly the column a reviewer looks at. Like `sessionDot`, any
git/parse error degrades to no badge. The git reads sit behind an injectable
`Server.pyramidGit` seam, exercised by mocked (`pyramid_mock_test.go`) and
real-git (`pyramid_render_test.go`) render tests.
