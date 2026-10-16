# The manager is a worker

> How the `wants:` **coordinator** stops being a special kind of ticket and becomes
> an ordinary worker whose *wake-subscription* is wider — a standing, addressable
> manager that owns the integration-level context, drains a queue of its children's
> lifecycle events, and conserves human attention by being the single thing on the
> board you talk to. This refines [`capabilities-and-supervision.md`](./capabilities-and-supervision.md)
> from lived experience with pre-digest: the machinery there is largely built, but
> the agent it wakes is never told it is a manager, cannot stand down without
> escalating onto the human's desk, and gives no visible sign it is managing. This
> doc settles those, and reverses that doc's "no special brief" line.

## The one-sentence claim

A manager is **not a second kind of ticket**. It is a worker woken by lifecycle
events on a **wider set of logs** — and that set is a monoid, so "manager" is a
value on one axis, not a type.

## The homogenisation — a subscription monoid

Everything that looked like a manager/worker *type* split collapses onto one axis:
**which logs wake you.**

- A **worker** is a dormant agent woken by lifecycle events on **its own** log
  (today: a `resolution` → `--resume`, via the `.path`-unit path-activation).
- A **manager** is a dormant agent woken by lifecycle events on **its own log ∪ its
  children's logs**.

The rest is data, not type:

| Apparent type difference | Actually |
| --- | --- |
| Manager brief vs worker brief | One assembly function projected over `wants:`: empty → "implement your spec"; non-empty → "here is your team, coordinate". |
| Manager verbs vs worker verbs | Identical `draiver` CLI. Which verbs get used is driven by the brief. |
| Authority to write into a child | Edge-derived (`P wants: C`), not type-derived. |
| "Don't ratify another's Review" | A uniform rule for *every* ticket. |

So the homogenised definition of a ticket is:

> **A dormant agent, resumed from its log, woken by lifecycle events on the set of
> logs it subscribes to, acting toward its own Review via the CLI.**

The **subscription set** is the only thing that varies, and it is a commutative
monoid:

- **carrier** — sets of `(ticket, event-type)` subscriptions;
- **operation** — union;
- **identity** — `{ my own log }` — the pure worker.

`wants: [C1, C2]` contributes `{C1, C2}`'s lifecycle events; a ticket's total
subscription is `own ∪ ⋃(wants)`. Because union is associative, **managers-of-managers
are free in the algebra** — a child that is itself a manager is just a deeper union.
We do **not build** recursion in v1, but the cap is one line in the wake computation,
not a type boundary (so the door stays open — see *Deferred*).

**One honest asymmetry.** A worker's wakes are mostly *resolutions* — answers coming
back. A manager's are mostly *escalations / reviews / dones* — others' status
changes. But both are "a lifecycle event on a subscribed log"; only the brief's
*interpretation* differs, and that is data. The mechanism is homogeneous.

## Designation — no new flag

- **`wants:` is the team roster** (frontmatter; immutable design input). Unchanged.
- **Manager-hood = `wants:` ∧ supervision mode.** The dial *is* the designator:
  **passthrough = manager off** (passive grouping / milestone; children escalate to
  the human — the safe default floor), **pre-digest = manager on** (proposer),
  **auto-execute = the future trust dial** (deferred).
- "Promote to manager" = flip the ticket to pre-digest. No new "I am a manager"
  field. The mode is a **mask over the subscription**: passthrough drops the
  child-escalation wakes (they route to the human); pre-digest keeps them.

The dial is therefore *not* dead weight: passthrough is the only way to say "group
and gate these, but I will handle the blockers myself," and it is the correct
default while pre-digest's resolve-rate is still being earned.

## The manager must be told it is a manager

This reverses `capabilities-and-supervision.md`'s *"It reads siblings via the CLI,
not a special brief… Nothing to build there."* That line is what made pre-digest
feel broken: a woken coordinator receives a byte-identical **worker** brief + worker
handbook, and `brief.Build` strips spec frontmatter — so the manager cannot even see
its own `wants:` children and defaults to **implementing its own spec**, the one
thing a manager must not do.

The fix is small and data-driven: branch the brief on `len(wants) > 0` to inject

- **`## Your team`** — each child with its control-state and open escalations (built
  from the existing `project.Control` fold, pointed at the children);
- **the coordinator protocol** — *your spec is a charter, not a work item; assess;
  `resolve --by` / `log --by` what you can; escalate **once**, consolidated, when the
  human is genuinely needed; then `park`*;
- **the wake reason for *this* wake** (see below) — "C3 escalated", "C5 reached
  Review", "the human answered your escalation", "all children Done — claim review".

`recall`/`brief` over the named children are adequate for cross-ticket context (no
`--team` rollup verb is needed) **once the roster is in the brief**.

## The manager's charter and its obligations

The manager **owns the integration-level context** — how the pieces fit, the
critical path, what has been tried. That context is its **work product**, written to
its *own* log and recalled on resume, exactly as a worker's code-and-decisions are.
This is why it is a real, portable ticket and not an inert rollup.

Its obligation on every wake is binary: **make the critical path unblocked and
progressing, or escalate once because the human's attention is genuinely required.**
That is both its acceptance-criterion analog and its **park guard**:

> **A manager may `park` iff the critical path is progressing and nothing in its
> queue needs it.** Otherwise it either acts (`resolve --by` / `log --by`) or emits
> one consolidated escalation.

## The lifecycle — generalised path-activation, the activation event, and a derived queue

### The wake is path-activation, widened

The "event-hook system" is not a new subsystem. It is path-activation widened along
the two axes the monoid already implies:

- **more logs** — the subscribed set (`own ∪ wants`), via the reverse-`wants:` index;
- **more event types** — wake on `escalation | review | done` on a subscribed log,
  not only `escalation` (today) and not only `resolution` on the own log.

It must be **edge-triggered**: wake on a *transition into* Review/Done, never on "is
in Review" (or the manager re-wakes every tick while a child sits there — the flap
the prior doc warns about). Edge-triggering needs a baseline — the spine-authored
**activation event** below.

### The activation event — "woken by C #20"

The dedup marker is **an event the deterministic spine writes on the manager's own log
at the moment it wakes it** — `woken by C #20` — *before* the agent runs. Putting the
mark in the spine, not the agent, is the load-bearing choice: the agent cannot cause a
wake-storm by forgetting to record progress, because it was never responsible for the
record. One entry does three jobs:

1. **Dedup** — a child *doorbell* (an `escalation|review|done` at some seq) with a
   matching activation event is handled; one without is still queued.
2. **Ordering** — activation events and doorbells are timestamped, so the spine replays
   them oldest-first.
3. **Wake reason** — it is the **tail of the cold-start brief** ("⟵ you were woken by
   C's escalation #20"), the freshest line the agent reads. The *same* artifact
   drvctl-053 injects into the brief and drvweb-023 renders on the board.

It is a durable **log event** on the parent (tier 2) because the brief replays from the
durable log; the mildly new thing is that **the reconciler authors it** (durable events
were agent-/human-authored until now — sound here, since only the spine knows the wake
happened and the decision is deterministic). The presence of *any* activation event also
tells the spine whether the manager has ever been activated — exactly what the
spawn-vs-resume choice needs.

### The queue and the replay — "blocked" does not mean "working"

The **queue is derived, never materialised**: it is the set of child doorbells with no
matching activation event. The spine's wake rule for a manager:

```
wake iff  enabled ∧ pre-digest
          ∧ (∃ child doorbell with no activation event)   // queue non-empty
          ∧ (the manager has no open escalation of its own) // not blocked on the human
```

The replay is **one doorbell per wake, oldest first**: the spine takes the oldest
unhandled doorbell, writes its activation event, wakes the manager; the manager handles
that one and parks; the spine takes the next. Serial by design — each wake has a single
clear reason — at the cost of more (cheap, cache-warm) spin-ups than a batch-drain; the
trade is deliberate and should not be "optimised" into a batch later.

The last clause is the whole of the "blocked on me" decision, settled as **queue, don't
parallelise**: while the manager has an open consolidated escalation (it is `Needs-me`),
the rule is false, so new doorbells simply accumulate unhandled. A manager is never
simultaneously "awaiting the human on A" and "working on C7" — the composite state and
concurrency that would require are deliberately avoided. When the human answers, the
`resolution` lands on the manager's own log, clears the escalation, and
**resolution-activation wakes it** (the existing primitive); the replay resumes at the
oldest still-unhandled doorbell.

**The one tradeoff.** Because the spine marks a doorbell handled at wake time, an agent
that wakes and does nothing (or crashes) leaves the child genuinely unhandled — its
escalation still open, it still Pending-"escalated to P" — yet the manager will not
re-wake for it. That is a silent *stall*, not a storm, and it is exactly the **staleness
watchdog** the design already flagged as a follow-on: a child left "escalated to P" with
no parent movement must be surfaced. So this choice makes that watchdog a required
companion, not an optional one.

### Endings (the completion gate must accept all three)

A manager ends a wake exactly as a worker does — only the handoffs differ in weight:

| Ending | Verb | Resulting state | On the human's desk? |
| --- | --- | --- | --- |
| Nothing needs me; path progressing | **`park`** *(new)* | Pending | No |
| One consolidated ask | `escalate` | Needs-me | **Yes — the single item** |
| All children Done, own AC met | `review` | Review | No (awaits ratification) |

The completion gate (`internal/completion`) today accepts only `review`/`escalate`
on the *own* log and is wired unconditionally into every session — so a manager that
assessed, delegated via `resolve --by` (which writes to the **child's** log), and
stopped gets nudged and then **escalated onto its own ticket**, straight to the human.
That is the bug that makes pre-digest net-negative. The fix: teach the gate the
`park` handoff → Pending, and reconcile it with the coordinator re-park
(`coordinator_gate` + reverse-`wants:` withdrawal) so a real agent, not just a test
fake, reaches Pending without escalating. `park` doubles as the roadmap's
stop/pause primitive.

## Escalation handling — the manager is the single desk item (Option B)

When the manager cannot resolve a child, it is **transitively escalated**: the child
escalation *moves* up (drvctl-050's move-not-copy) to Pending — *"escalated to P"* —
**off the inbox**, and the manager emits **one consolidated, integration-level**
escalation. The human resolves the **manager**, which relays the answer down. This is
chosen over "leave the child escalated and make the human look at the child" for two
reasons, each of which also disposes of an objection:

- **Not noisy.** Escalation *moves*, not copies: the inbox holds exactly one item —
  the manager. The invariant is *"at most one ticket per escalation path in the human
  inbox — the highest awake manager."*
- **Not micromanaging.** The human answers the manager at the integration level
  ("the endpoint is X"); the manager translates that into child resolutions. The
  human never touches a child — that is delegation, the opposite of micromanaging.

"Make the human look at the child" survives **only as degradation**: if the manager
itself is non-functional (StartLimit ceiling), the whole path — child included —
surfaces to the human.

## The manager's executive power

Three verbs, all edge-authorised by `P wants: C`, suffice for "sort the critical path
and assign work":

1. **Transitive enable** — manager enabled ⇒ children enabled (already built).
2. **`resolve --by <P>`** — unblock a child's escalation (built).
3. **`log --by <P>`** *(new)* — write durable **direction / context** into a child's
   log, not only a resolution.

The bright line is unchanged and uniform: **edge-authorised writes = {resolution,
log-fact}, never {review, done}** — *direct-or-unblock ≠ ratify*. Enabled + the
manager directing/unblocking **is** assigning and delegating work; it is safe because
the manager can never approve a child's Review. (Reopening a child that has already
reached Done/Review is **not** covered here — that is the deferred
new-attempt-on-retrigger, signalled by the integration test failing.)

## The UI signal — the control plane already knows why

The deterministic spine woke the manager for a specific, known reason — and it already
wrote that reason down as the `woken by C #N` activation event (above). The board reads
the latest activation event for the "why" and derives the **queue depth** (unhandled
doorbells) for the count, rendering the coordinator distinctly — *"manager — on it,
re: C3 · 2 queued"* — instead of an anonymous running ticket; the capability panel shows
the manager's own current action, not only the children's states. The *same* activation
event is the tail of the brief: **one source, three consumers** (dedup, brief, board). (The manager's
moment-to-moment action within a wake — assess vs. resolve vs. consolidate — is
stochastic and optional; the deterministic wake reason is the 80% that makes the board
read as "someone is handling it.")

## Tier discipline recap

| New state | Tier | Form |
| --- | --- | --- |
| `wants:` roster | static | `spec.md` frontmatter (existing) |
| Supervision mode | mutable declaration | log event (existing) |
| Activation event (`woken by C #N`) | durable semantic | **log event, spine-authored** (new) |
| Derived queue (doorbells w/o activation) | derived | computed on read — stored nowhere |
| Wake reason | runtime/session | derived at render — no log event |
| `park` | lifecycle handoff | a turn-end the completion gate accepts → Pending (new) |

## What is genuinely new to build

Most of this is recombination of shipped primitives. The new parts:

1. **Manager brief** — branch `brief.Build` on `wants:`; stop stripping edges; inject
   `## Your team`, the coordinator protocol, and the per-wake reason. (Reverses "no
   special brief".)
2. **`park` + completion-gate reconciliation** — a self-park handoff → Pending with
   no escalation; make the gate coordinator-aware so a delegating-then-stopping
   manager is not escalated.
3. **Generalised path-activation + the `woken by` activation event + queue/replay** —
   widen the wake to `escalation|review|done` over the subscribed set, edge-triggered;
   the spine writes a `woken by C #N` event at wake time (dedup + ordering + brief-tail
   wake reason); derive the queue as unhandled doorbells and replay oldest-first; gate
   on "no open own-escalation". The keystone — needs the staleness watchdog as a
   companion.
4. **`log --by`** — extend the edge-authorised cross-ticket write from `resolve` to
   `log`; bright line {resolution, log-fact} ∌ {review, done}.
5. **Wake-reason UI** — read the activation event + derive queue depth; render on the
   board card and the capability panel.
6. **`park` as a general primitive** — an abstract wake source so a plain worker can
   `launch-async && park` (the test runner is the first consumer, drvctl-055), not just
   a manager; refuses to park with no wake source. Kills the false-escalation where a
   test-wait masquerades as a human question.

## Implementation tickets

Multiple — this is **not** one ticket. It crosses the brief, the CLI, the reconciler,
the completion gate, and the UI; bundling them is a large, multi-surface change
against the per-seam discipline the drvctl-037..042 split already set. The natural
decomposition and DAG:

| Ticket | Title | Depends on |
| --- | --- | --- |
| **drvctl-051** | **Generalised activation + the `woken by` event + queue/replay rule.** Widen reverse-`wants:` to `escalation\|review\|done`, edge-triggered; the spine writes a `woken by C #N` activation event on the parent at wake time (dedup + ordering + brief-tail wake reason); derive the queue as doorbells without an activation event and replay oldest-first; suppress while the manager has an open own-escalation. Ships the staleness-watchdog follow-on. The keystone. | 041, 050, 052 |
| **drvctl-052** | **`draiver park` — the general suspend primitive + completion-gate reconciliation.** A self-park handoff → Pending with no escalation, built around an **abstract wake source** (not hard-wired to `wants:`) and **refusing to park an attempt with no pending wake source**. Teach `internal/completion` to accept it and to not escalate a `wants:` ticket that delegated via `resolve --by`. Correctness fix for pre-digest; also the roadmap stop/pause primitive. | 042 |
| **drvctl-053** | **Manager brief.** Branch `brief.Build` on `wants:`: stop stripping edges, inject `## Your team` + coordinator protocol + the activation-event wake reason (the brief tail). Reverses "no special brief". | 051 |
| **drvctl-054** | **`log --by <parent>`.** Edge-authorised cross-ticket `log` (reuses the `resolve --by` authorisation + provenance stamp); bright line excludes review/done. | 050 |
| **drvctl-055** | **Park-pending-own-async-work (test runner as first consumer).** Detached/tracked `draiver test` that emits a durable result event; the worker does `launch-async && draiver park`, woken when the result lands on its own log — reusing 052's abstract wake source and 051's `woken by #N`. Proves `park` generalises past the manager and kills the false-escalation (a test-wait masquerading as a human question). | 051, 052 |
| **drvweb-023** | **Manager signal in the UI.** Read the latest activation event for the "why" + derive queue depth; render a distinct coordinator card ("manager — on it, re: C3 · N queued") and the manager's own action line in the capability panel. | 051, 052 |

Land order: **052 → 051 → 053**, then **054**, **055**, and **drvweb-023** in parallel.
052 first because 051's re-park (and 055's worker-park) are only correct once the gate
stops escalating a stopping attempt; 053 is most valuable once 051 can supply the
activation event it injects as the brief tail.

## Deferred / out of scope

- **Recursive managers** (managers of managers). Free in the algebra; the one-level
  cap is a line in 051's wake computation, lifted later, not a redesign.
- **Auto-execute** — the trust dial; enable per class only once pre-digest's
  resolve-rate is measured.
- **New-attempt-on-retrigger** — reopening a child that already reached Done/Review;
  the fire-once latch stands, staleness surfaces at the integration test.
- **Proactive heartbeat cadence + budget governor** — a manager is already under the
  same budget/StartLimit governance as any session, so this is *tuning*, not
  architecture; mapped later.
- **`recall --team` sugar** — unnecessary once the roster is in the brief.

## Prior decisions this changes

- **`capabilities-and-supervision.md` — "no special brief" is reversed.** The
  coordinator does get a `wants:`-branched brief (drvctl-053). Reading siblings via
  the CLI is still how it fetches *detail*, but it must be *told it is a manager* and
  shown its roster.
- **The reverse edge wakes on more than failure.** `capabilities-and-supervision.md`
  wakes the coordinator only on a child *escalation* (the `OnFailure=` analog).
  drvctl-051 widens this to Review/Done so the manager can advance the critical path,
  not only firefight.
- **The "blocked on the human" question is settled as queue-not-parallelise** — a
  manager with an open escalation does not wake on new child events; they queue
  behind the watermark and drain when the human answers.
