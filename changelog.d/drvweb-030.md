---
section: Changed
---
**The board caches each attempt's git state in memory, so an idle webui stops running git for every card on every poll (drvweb-030).**
The test-pyramid badge used to start several `git` subprocesses per card on every 3s
board poll: about 2.2s of a 2.7s render over roughly 200 mostly-Done attempts. A
transparent in-memory cache now sits in front of those reads (`internal/web/pyramidcache.go`).
It changes how often git runs, never what is rendered. An entry is reused only while a
cheap check says its inputs are unchanged. Branch tips are read straight from the ref files
(loose refs and `packed-refs`) with no subprocess. Live worktrees come from one
`git worktree list` per repo. The dirtiness of live worktrees is sampled once per render,
in parallel. The overlay revision is resolved the same way, and the operator config is
memoized on its file stat. A commit, a dirtied or reclaimed worktree, or a moved overlay
shows up on the very next render, not after a timeout. A dirty worktree is never served
from the cache, so a green badge still means "clean at HEAD". Push, sync, promote,
review-env and mark-done drop the attempt's entry so their own response re-reads git.
Failed reads are cached for only 2s, concurrent renders share one git run per attempt,
and idle entries are evicted. Warm renders of a 200-attempt fixture fall from about 1.2s
to about 50ms. On a real data root the warm render takes about 0.3s, down from 2.3s.
Repos using the reftable ref backend skip the cache and read git directly, as before.
