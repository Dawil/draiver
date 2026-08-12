# Capabilities & supervision

> How a parent **Capability** ticket coordinates its sub-tickets through dependency
> edges and an event-driven **coordinator agent** — and the per-ticket dial for how
> much that coordinator does *before* a human is involved. This is the settled
> design; [`targets-and-dependencies.md`](./targets-and-dependencies.md) is the
> exploratory precursor that worked the forks out.

## The shape

A Capability with source code, infra, and an integration test:

```
CAP-A/spec.md      (Jira epic; coordinator; no-repo or multi-repo)
  wants:    [SRC-1, INFRA-1, E2E-1]
├── SRC-1          (source)
├── INFRA-1        (infra;  after: [SRC-1])
└── E2E-1          (integration test;  after: [SRC-1, INFRA-1])

CAP-B/spec.md      requires: [CAP-A]     # a later capability, gated on A being Done
```

Enable `CAP-A`. Its `wants:` pulls the three sub-tickets into the fleet. `SRC-1`
runs; `INFRA-1` and `E2E-1` sit **Pending** behind their `after:` gates. `SRC-1`
reaches Review → `INFRA-1` admits; both reach Review → `E2E-1` admits and runs the
integration test. If the test fails, `E2E-1` **Escalates** with a concrete
diagnosis; that escalation **wakes `CAP-A`** (dormant until now), which assesses
across all three sub-tickets and — depending on its **supervision mode** — either
routes the block to a human or pre-digests it into a recommendation the human
ratifies. `CAP-A` is **Done** when its coordinator claims Review and a human
ratifies — never by rollup.

Everything below is the machinery behind that paragraph.

## Prior decisions this builds on

Settled in [`targets-and-dependencies.md`](./targets-and-dependencies.md); not
re-argued here:

- **The coordination root is a real ticket, not an inert aggregator.** A ticket's
  state is always derived from *its own* log/session, never rolled up from
  children — the portability invariant.
- **Authored edges live in `spec.md` frontmatter** (immutable design input, shared
  across attempts). Runtime-discovered edges live in the `log/`. Derived state
  lives nowhere.
- **Admission is fire-once and edge-triggered** (a satisfied gate is a latch; a
  later change in a dependency does not auto-restart the dependent — staleness
  surfaces at the integration test).
- **Cross-ticket mutation is human-gated: escalate-and-recommend.** An agent never
  writes into another ticket's log; it proposes, a human ratifies, `ctld` executes.

## The three relations

Draiver keeps systemd's names, bound to draiver **control states** (systemd has no
Review/Done — a native reinterpretation, not a literal port):

| Field on ticket X's `spec.md` | Points to | Meaning |
| --- | --- | --- |
| `wants:` | tickets X pulls in | *"If I am enabled, enable them."* The grouping / `.target` edge. **Enable flows down it; escalation flows up it** (see below). |
| `after:` | tickets X waits on | *"Admit me once they reach **Review**."* Ordering gate on the success *claim*. |
| `requires:` | tickets X waits on | *"Admit me once they reach **Done**."* Ordering gate on the *merged* terminal. |

Every field is owned by the ticket the relationship belongs to; a parent typically
carries `wants:`, a child `after:`/`requires:`. Because a Capability is only a
ticket, any ticket may carry any of them.

**Where the analogy stops:** vanilla systemd `After=` is pure ordering and
`Requires=` adds failure propagation. Draiver reuses the names for two ordering
gates differing only by which control state they wait for (Review vs Done). A
failed `requires:` predecessor **culling** its dependents is *not* in v1.

## The two edge directions (the part that was missing)

`after:`/`requires:` are a **forward success gate**: a predecessor reaching
Review/Done wakes its *successor*. They deliberately do **not** wake a successor
when a predecessor *escalates* — if `SRC-1` is stuck, `E2E-1`'s inputs aren't ready
and it *should* stay Pending.

The thing that should wake on a sub-ticket's escalation is the **Capability**, via
a **reverse edge** — systemd's **`OnFailure=`** ("units activated when this one
fails"). Rather than add a field, **reuse `wants:`**: enable flows *down* the edge,
escalation flows *up* the same edge.

> **Activation rule:** on an **escalation** event in ticket X's log, activate every
> Pending ticket W whose `spec.md` `wants:` X.

Children stay fully decoupled — they never name the coordinator (a Jira task doesn't
reference its epic to report a blocker; the epic owns the relationship).

**This extends machinery that exists; it is not a new subsystem.** `draiverctl.md`
already makes the log dir a `.path` unit: *"a new `resolution` event fires a resume
session."* Today the trigger is "a `resolution` in **my own** log wakes me." The
addition is one rule plus one reverse index over `wants:` — "a wanted child's
**escalation** wakes **me**." Loop safety is already covered: a coordinator that
re-wakes in a cycle hits the existing **StartLimit ceiling → escalate to human**,
and its output is human-gated regardless.

## The Pending state

A fifth control state, below Needs-me on the attention scale:

- **Pending** — enabled/desired, **no live agent, no escalation**, advancing
  automatically when a gate opens or an activation fires. It requires **zero human
  attention** (Needs-me needs a human; Pending needs nothing) and renders as a quiet
  count.

**"No agent" is not the new part.** Needs-me, Review, and Done already have no live
agent — a Needs-me ticket escalated (exit 3), its session ended, and it waits for a
resolution to reactivate. What's new is a **self-resolving wait**: it advances on a
*system* event, needing neither a human nor a running agent.

**Derivation & tier.** Pending is a **runtime/derived** projection — "enabled + no
live session + last lifecycle event isn't escalation/review/done" — computed on
read, like the session substates. It is **not** a log event, so the hash chain stays
clean (the tier discipline). Its core derivation is local; only the human-facing
*reason* ("waiting on SRC-1") peeks at a sibling's state, and that peek is a
session-tier computation.

**systemd mapping.** systemd splits this across concepts, which is why no single
word ports: a dependency-ordered **job** sits in state `waiting` until predecessors
finish; a path/socket-activated unit is `inactive (dead)` while its activator
watches; a `.target` reaches `active` with no process (the rollup we avoid). Pending
collapses the first two into one board-visible state.

## The coordinator

The Capability ticket is a **normal ticket** (own repo or none, own log, portable),
distinguished only by an **event-driven lifecycle**:

```
Pending ──(child escalates)──▶ Running (assess + escalate-and-recommend) ──▶ Pending
   │                                                                            │
   └───────────(all children Done)──▶ Running (final review claim) ──▶ Review ──▶ Done
```

- **It reads siblings via the CLI, not a special brief.** The coordinator agent has
  `draiver` like any agent; for cross-ticket context it runs `draiver brief OTHER`
  or reads the logs itself. Nothing to build there.
- **It is a proposer, not an executor** (escalate-and-recommend). The deterministic
  `ctld` and the human own execution — the line that keeps this from being
  stochastic-all-the-way-down.
- **Dormancy is the cattle/pet split, verbatim.** Between wakes the session is
  reaped (cattle); the attempt/log persists (pet); a wake `--resume`s from `brief`.
- **Completion is its own review claim, not a rollup.** When it assesses all children
  Done and the e2e green, it Reviews; a human ratifies Done.

It **oscillates** through Running many times rather than running once — the one way
its lifecycle differs from every ticket today. Every constituent piece
(dormant-with-persisted-log = Needs-me; wake-on-event = path-activation;
session-is-cattle) already exists; the coordinator is their recombination.

## The supervision dial — passthrough → pre-digest → auto-execute

How much the coordinator does *before* a human is not one global truth — it is an
**opinion that varies by person and by ticket**, so it is a **per-ticket mode with a
configurable default** (mirroring how `enable` is a per-ticket override of a global
"disabled by default").

We are **not removing the human** — only removing what a human doesn't *have* to do,
to conserve their attention. The human moves from first-responder to ratifier, and
the ratification gate stays until trust is earned.

| Mode | On a sub-ticket escalation | Human's role | New machinery |
| --- | --- | --- | --- |
| **Passthrough** | Goes straight to Needs-me; no coordinator wakes. The Capability is a Pending shell that wakes only for its final review. | First-responder & router | none beyond the DAG |
| **Pre-digest** *(the incremental step)* | Reverse-`wants:` wakes the coordinator; it assesses across children and posts one **consolidated recommendation**. | Ratifier | coordinator agent + activation |
| **Auto-execute** *(future trust-dial)* | The coordinator applies a **whitelisted class** of reopens itself; the rest still escalate. | Exception-handler only | auto-apply path + whitelist + audit |

- **Passthrough** is the floor — it works with today's primitives plus the DAG.
- **Pre-digest** is the genuine first step into agentic supervision, and the target
  for v1's coordinator.
- **Auto-execute** is deliberately **deferred**: enable it per class only once
  pre-digest's recommendation-accuracy has been *measured*. It is the only mode that
  lets an agent effect a cross-ticket mutation without ratification, so it needs the
  data first.

**Where the mode lives.** Like `enable`/`disable`, the mode is a *mutable* control
setting, so a global/project default lives in config and a per-ticket override is a
**log event** (mutable declaration → log, not frontmatter).

## Attempt admission (carried over)

- **Which attempt.** Transitive enable / a satisfied gate targets the ticket's
  **latest** attempt; if none (or the latest is terminal) it creates one with the
  **default launch config**. No launch-config customization in v1.
- **Fire-once latch.** Once admitted, a dependent is not auto-restarted by a later
  change in its dependency; staleness surfaces at the integration test. The future
  path for auto-freshness is **a new attempt on retrigger**, not an in-place restart.

## Cycle safety

`wants:`/`after:`/`requires:` form a DAG; the edge-authoring path (CLI + UI) must
**detect and refuse** a cycle rather than trust human vigilance — cheap to check,
nasty to debug. Runtime activation additionally leans on the StartLimit ceiling.

## What is genuinely new to build

Most of this is recombination. The new primitives, in full:

1. `wants:`/`after:`/`requires:` frontmatter + a reverse index over `wants:`.
2. Transitive enable down `wants:`.
3. The Pending projection.
4. The forward success gate (`after:`/`requires:` hold admission until Review/Done;
   edge-triggered latch).
5. Reverse-`wants:` activation (a child's escalation wakes a Pending parent) — the
   `OnFailure=` analog, extending path-activation.
6. The coordinator lifecycle + the supervision mode (passthrough / pre-digest;
   auto-execute deferred).
7. Cycle detection at the authoring seam.

## Implementation tickets

All Tier-2 (fleet) work per `draiverctl.md`, extending its dependency-DAG row with
the coordinator + supervision dial. Engine = `drvctl`; UI = `drvweb`.

| Ticket | Title | Depends on |
| --- | --- | --- |
| **drvctl-037** | Dependency edges in `spec.md` (`wants`/`after`/`requires`): parse + authoring verb (reusing the `title` frontmatter-merge path) + reverse-`wants` index + cycle refusal at author time. Data model only — no reconciler behavior. | — |
| **drvctl-038** | Transitive enable/disable down `wants:` — desired-ness flows to children (latest attempt, or create with the default launch config); extends `desired()` with the enabled-via-parent path. | 037 |
| **drvctl-039** | The **Pending** control state — derive in `project` (enabled + no live session + not blocked/review/done), a runtime projection (no log event); `status` prints it. | 037 |
| **drvctl-040** | Forward success gate — reconciler holds a Pending ticket out of admission until `after:` predecessors reach Review (or `requires:` reach Done); admit on gate open; **edge-triggered latch** (fire-once). First scheduler increment. | 037, 039 |
| **drvctl-041** | Reverse-`wants:` activation — an `escalation` event in X's log wakes every Pending ticket that `wants:` X (spawn/resume its coordinator session); StartLimit ceiling applies. The `OnFailure=` analog. | 037, 039 |
| **drvctl-042** | Supervision modes **passthrough** & **pre-digest** — mode as global/project config default + per-ticket log-event override; passthrough routes escalations to Needs-me; pre-digest wakes the coordinator to assess siblings and post one consolidated escalate-and-recommend. Auto-execute out of scope. | 041 |
| **drvweb-015** | Board: render **Pending** — a quiet zero-attention count/column, one card per Pending attempt, with the "waiting on X" reason. | 039 |
| **drvweb-016** | Edit dependency edges in the UI — reuse the drvweb-009 provenance inline-edit style, but **scoped to `spec.md`** (ticket-level) on the attempts-index page, backed by the drvctl-037 verb; surface cycle-refusal inline. | 037 |
| **drvweb-017** | Capability view — on a Capability ticket, show its `wants:` sub-fleet (child states + gates) and a control to set the supervision mode. | 038, 042, 015 |

Follow-ons (noted, not scheduled): `focus`/`isolate` a Capability (roadmap Tier 2);
**auto-execute** mode + whitelist + audit (the trust-dial, after pre-digest data);
**new-attempt-on-retrigger** (auto-freshness beyond fire-once).

## Open questions

- **Auto-execute whitelist** — what class of cross-ticket reopens is ever safe to
  apply without ratification, and what audit trail does it leave? Deferred until
  pre-digest accuracy is measured.
- **Multi-repo coordinator checkout** — if a coordinator ever needs to *run*
  something (vs. only read logs + recommend), does it get a multi-repo worktree, or
  is "runs the e2e" always a separate `after:`-gated ticket? Leaning the latter.
