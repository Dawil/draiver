---
section: Added
---
**The board renders the Pending control state with a "waiting on X" reason
(drvweb-015).** The zero-attention Pending tier from drvctl-039 now appears on
the web board (`GET /`, `/board`). It is a quiet count nested *under* Running in
the same vertical rather than a fifth column, with one card per Pending attempt.
`board()` now fills the runtime `Desired`/`Live` bits and switches on
`a.Control()`, the same overlay `draiver status` applies. A desired, log-Running
attempt with no live agent therefore lands in the Pending bucket. Liveness is
checked through the injectable `s.alive`, so it agrees with the session dot.
Each card shows a quiet reason line from the new
`project.WaitingReason(ticket, all, edges)` (stored on a runtime
`Attempt.WaitingReason` field). The function works like a read-side copy of
the forward gate: it names the predecessors still holding the gate shut
(`after:` short of Review, `requires:` short of Done) as *"waiting on SRC-1"*.
If no edge is holding the attempt and it is only waiting for admission, it
reads *"waiting to start"*. Stable testids are `col-pending`, `count-pending`
and `waiting-<ticket>-<attempt>`. The usual 3s board poll recomputes
`Control()` on every render, so a card moves from Pending to Running when its
gate opens and an agent is admitted. The e2e seed gains a Pending fixture with a
Playwright assertion. This change also repairs `e2e/global-setup.ts`, which had
been broken since `--repo` became required at creation.
