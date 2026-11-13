---
section: Added
---
**Green play button on the board's Running column, the webui's first write
(drvweb-005).** A Running attempt that is not yet enabled (the supervision axis
the grey session-dot reads) shows a green play button on its card; clicking it
opts the attempt into daemon supervision and the card re-renders in place (the
button disappears, the dot flips off grey). The write is not reimplemented in
the web layer — `POST /ticket/{id}/{attempt}/enable` shells `draiver ctl enable`,
the same verb a human runs at a terminal, so there is a single enable code path.
Bare enable only: a running `ctl up` brings the attempt up on its next tick, and
with no daemon the enable simply waits (no control socket in the web process).
The write is attributable (`--actor` / `$DRAIVER_ACTOR`, else `human:webui`) and
same-origin guarded; it is idempotent (an already-enabled attempt is a no-op).
