---
section: Fixed
---
**Resolve textarea keeps focus and caret across the 3s log poll (drvweb-014).**
On the attempt page the inline escalation **resolve** `<textarea>` lives inside
the `log-region`, which htmx re-fetches and swaps (`innerHTML`) every 3 seconds.
`hx-preserve` kept the typed *value*, but htmx relocated the preserved node
during the swap, blurring it and dropping the caret — so the cursor jumped out
of the box every ~3s and multi-word answers were nearly impossible to type. A
small inline handler in `internal/web/templates/ticket.html` now snapshots the
focused `.resolve-input` (id + selection) on `htmx:beforeSwap` and, on
`htmx:afterSwap`, refocuses it (`preventScroll`) and restores the caret via
`setSelectionRange` — synchronously, before paint, so there is no flicker. The
log region still updates on its cadence, value retention is unchanged, and the
input still vanishes once the escalation is resolved; the compose textarea sits
outside the polled region and was never affected. Locked by
`TestResolveInputKeepsFocusAcrossPoll`. (The approach was later reworked onto
idiomorph in drvweb-026 after the morph swap reintroduced the bug.)
