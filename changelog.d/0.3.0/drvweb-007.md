---
section: Added
---
**Inline escalation resolution from the board (drvweb-007).** Each *open*
escalation on the attempt timeline now renders a resolution textarea + Resolve
button; submitting appends a `resolution` event refing that escalation's seq,
clears the block, and leaves Stuck — the board affordance for `draiver resolve`.
A resolved escalation shows only "→ resolved by #N". Like the other webui
writes, it is not reimplemented in the web layer: `POST
/ticket/{id}/{attempt}/resolve` shells `draiver resolve` (runDraiverResolve, via
a shared `draiverExe()` helper), so the board and a terminal share one append
path. Guards mirror the CLI and close the gap it leaves: same-origin CSRF (403),
empty answer (400), and `classifyResolveTarget` rejects an unknown or
non-escalation seq (400) and an already-resolved seq (409), all read from
derived state rather than the posted form. The textarea carries a stable id +
`hx-preserve` so the 3s poll swap never clobbers a half-typed answer and
correctly vanishes once resolved. The webui is now a three-write surface (log,
resolve, enable).
