---
section: Added
---
**Cross-attempt, per-repo prompt-cache rollup with A/B cohorts (drvweb-013).**
The caching win is cross-ticket amortisation — every attempt after the first on
a repo reads the shared prefix at ~10% — which the per-attempt cache panel
(drvweb-012) cannot show. A new **`/cache`** page, linked from the board topbar
("Cache rollup →"), aggregates every metered attempt per repo: hit ratio, cache
read/written, normalised work, billed-vs-uncached input-equivalents, and saved
fraction. Aggregation (`repoRollups` in `internal/web/rollup.go`) is pure and
deterministic and sums raw token counts before deriving ratios — never averaging
per-attempt ratios. **A/B cohorts** split attempts by whether caching actually
engaged (`agent.Metrics.CachingActive`) and compare the on/off cohorts side by
side on normalised cost and outcome (terminal-state distribution). A per-repo
**caching-health line** reads `ok` when reads dominate, `warn` when cache
creation dominates (the silent-invalidator smell of a busted shared prefix), and
`off` when caching never engaged. Because no per-attempt cache-config tag exists
yet (drvctl-032 deferred it), a flag-on run whose prefix busted lands in the off
cohort; the page caption says so. The "saved" projection (`cacheCost`) is now
shared with the per-attempt panel so the figure means the same on both. The page
is a static report, not a polled region.
