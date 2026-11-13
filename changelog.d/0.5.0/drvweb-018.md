---
section: Added
---
**Cache-win metrics on the attempt prompt-cache panel — cost avoided, billed vs
uncached, and the reuse factor (drvweb-018).** The drvweb-012 panel led with
`cache_hit_ratio`, a whole-session average bounded at 1 that understates reuse and
carries no cost figure. `agent.Metrics` gains three derivations from the existing
`Totals` (no new capture): **`ReadCreationRatio`** (`cache_read/cache_creation`,
the unbounded ">100%" reuse factor; a `*float64` that is `null`/"—" when nothing
was written), **`CostAvoidedInputTokens`** (uncached baseline minus
`BilledInputTokens()`, i.e. `0.9·read − 0.25·creation`, deliberately unclamped so
a write-never-read session shows negative) and **`PctCostAvoided`** (that as a
share of baseline). The panel now leads with **equivalent API cost avoided** ($
and % of prompt spend — a modelled figure, since the daemon runs on subscription
OAuth), a **billed-vs-uncached** dollar bar, and the **reuse factor** (`×N` /
`N%`); the old ratio is relabelled "share served from cache", and every metric
carries a provenance-style `(i)` hover explaining it and what a good result looks
like. A minimal input-price lookup in `internal/web/pricing.go` prices
opus-4.8 at $5/1M; an unknown model falls back to input-equivalent tokens with no
`$`. `attempt.parseMeta` recomputes the derived fields from the raw token counts,
so attempts metered before this change no longer read as `0`. Because attempts
don't yet persist a resolved model id, live panels currently show the
token-equivalent figures rather than dollars.
