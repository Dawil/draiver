---
section: Added
---
**Transitive enable/disable down `wants:` — desired-ness flows to children
(drvctl-038).** The reconciler's `desired()` gains a third path,
**enabled-via-parent**: an attempt is desired if it is directly enabled *or* a
ticket that (transitively) `wants:` it has a desired attempt. Enabling a
Capability with `wants: [A, B]` now pulls A and B into the supervised fleet
alongside it — and grand-children through an intermediate the parent only
*wants* (not one that is itself directly enabled). For each wanted child the
loop targets its **latest attempt** when that is `Running`; a child with **no
attempt yet** gets one **minted with the default launch config** (the
`ctl up --repo` fallback, default adapter, base-spec model — the same start a
manual `enable` gives it). Propagation is a **derived** computation re-run every
tick, not a stamped bit, so **disable withdraws symmetrically for free**: a
disabled parent simply stops sourcing its children next tick, while a child that
is *also* directly enabled stays desired. A child whose latest attempt is a
Review claim, a Needs-me block, or a closed Done is **left alone** — parent
desired-ness never force-admits a non-Running attempt nor re-mints over a
terminal one (auto-freshness on a spent child is the deferred
*new-attempt-on-retrigger* follow-on). Cycles remain refused at authoring
(drvctl-037); a visited-guard keeps a hand-edited cyclic spec from spinning
regardless. `enable`/`disable` stay a pure per-attempt log bit — no fan-out is
written to disk.
