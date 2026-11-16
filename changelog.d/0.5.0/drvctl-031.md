---
section: Added
---
**Per-attempt prompt-cache telemetry — cache tokens persisted, derived and
priced (drvctl-031).** The adapter already read `cache_read` /
`cache_creation` per request (drvctl-012), but nothing surfaced them per
attempt. `session.Meter` (`meter.json`) now carries a cumulative `Totals`
alongside the live-gauge `Usage` snapshot: `watch` sums input, output,
cache-read and cache-creation tokens from the **per-request frames only** (the
cumulative turn-end frame is skipped, so a turn is never double-counted), and
the totals accumulate across turns and respawns. `agent.Totals` derives
`cache_hit_ratio` (`cache_read / (cache_read + cache_creation + input)`),
`normalized_work` (all tokens at 1x, for A/B comparisons), `billed_input_tokens`
and `caching_active` (true iff any cache field is non-zero — catches
below-min-prefix and silent-invalidator misses). The derived fields are written
to the file but always recomputed from the raw counts on read, so they cannot
desync. On every retire (terminal or park) the reconciler folds the final tally
into `attempt.md` as a `metrics` block — the reserved metrics room made real.
Pricing lands as exported multipliers (cache read 0.1x, write 1.25x for 5m TTL /
2x for 1h TTL); dollar cost still comes from the adapter's `total_cost_usd`,
which is already cached-rate priced, so no existing quota math changed. This is
the measurement foundation for the rest of the prompt-caching set
(`docs/prompt-caching.md`).
