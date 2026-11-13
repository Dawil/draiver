---
section: Fixed
---
**Board and live log keep your text selection across the 3s poll
(drvweb-025).** The board (`#board-region`) and the attempt live region both
polled every 3s with `hx-swap="innerHTML"`, which replaced the whole subtree on
every tick and wiped any text selection, so you could not copy from the board or
the live log. Both regions now swap with `hx-ext="morph"` /
`hx-swap="morph:innerHTML"` using a vendored idiomorph 0.8.0 extension
(`internal/web/static/idiomorph-ext.min.js`, served from the embedded static
FS, no CDN). Morph diffs the incoming fragment against the live DOM: a poll with
no changes makes zero mutations, so selection, caret and `:target` survive, and a
changed cell updates in place without disturbing a selection elsewhere. ETag/304
was rejected because htmx still swaps a revalidated body. Two workarounds from
the `innerHTML` era were removed: the resolve-textarea focus/caret snapshot
script and the `.is-target` mirror (native `:target` now persists, and the CSS
highlight is just `.event:target`). Out-of-band badge/count updates and the 286
poll self-cancel still work. Covered by
`features/polling-morph/selection.feature` and Playwright specs that select text,
wait out a real poll, and assert the selection survives. (The textarea's
remaining `hx-preserve` was removed later by drvweb-026.)
