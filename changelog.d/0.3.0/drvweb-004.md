---
section: Added
---
**Review links surfaced as one-click actions on the board (drvweb-004).** A
Review-column card whose latest `review` event carries a link now shows a
distinct **"Review changes ↗"** action — the primary link (`rel` `pr`/`mr`, else
the first) — opening the PR/diff in a new tab (`target="_blank"
rel="noopener noreferrer"`), *additional to* and visually separate from the
existing internal deep-link. No link → no button (a direct-merge flow degrades to
nothing). The action is scoped to the Review column, so a reopened or blocked
attempt never shows a stale PR button. On the attempt detail timeline, every
event renders its links as chips labelled by `rel`, below the body. Links are
rendered **safely**: the http/https scheme allowlist is **re-checked at render**
(defense in depth, independent of the append-time check), and the board never
fetches or previews the URL.
