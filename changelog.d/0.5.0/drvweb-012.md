---
section: Added
---
**Prompt-cache panel on the attempt detail page (drvweb-012).** The attempt
view (`/ticket/{id}/{attempt}`) gains a cache panel above the spec that shows the
drvctl-031 telemetry, so checking whether caching worked no longer means reading
`meter.json` by hand. `caching_active` is the headline yes/no, followed by cache
read vs written tokens, hit ratio, normalised work, and billed vs uncached
input-rate token equivalents with the fraction saved. These are token
equivalents, not dollars, because the metrics carry no price. A
`caching_active: false` attempt is visually distinct: the panel turns amber
(`.cache-inactive`) and the flag shows ✕ "the prefix never cached", so the
signal does not rely on colour alone. The metrics are only folded in when an
attempt retires with a metered session, so an in-flight attempt shows a quiet
"recorded when this attempt retires" placeholder instead of a grid of zeros.
`project.Attempt` now exposes the folded `agent.Metrics`. The cross-attempt
amortisation view is left to drvweb-013.
