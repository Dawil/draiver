# Draiver is a Belief Store Wired Between Two Reconcilers

> Draiver began as a tool for coding agents on tickets. One recent change — letting
> a log **supersede** earlier facts — quietly turned the ticket log into something
> more general: a **belief store**, a thing that answers *"what should I act on as
> true right now, and why?"* for a stateless consumer. Seen whole, draiver is that
> belief store wired between **two reconcilers pointing in opposite directions** —
> one that *acts* on the world (agent drives code → spec) and one that *senses* it
> (observations drive belief → reality) — closed into a **supervised control loop**
> with the human as the thing that keeps it stable. This is GitOps for a world you
> don't fully know, worked by reasoners you can't fully trust.

The companion docs place draiver against the industries it rhymes with —
[`coding-agent-platform.md`](./coding-agent-platform.md) (platform engineering),
[`control-plane.md`](./control-plane.md) (a control plane whose scarce resource is
human attention), [`prompt-caching.md`](./prompt-caching.md) (the wall, and why the
protocol lives above it). This document works out a different rhyme — **GitOps and
closed-loop control** — and what it says about the knowledge draiver accumulates
and the stability of the loop it runs.

It is a *conceptual* note, not a spec. It describes a shape draiver is growing into,
not a feature to go build. The discipline throughout is the one that produced the
insight: generalize only the piece a real use case demands, and make the thing you
started with fall out as the simplest special case.

## How a ticket log became a belief store

The ticket log was always append-only and hash-chained — event sourcing with a
tamper-evident chain (see [`control-plane.md`](./control-plane.md)). What it was
*missing* to be a belief store was **defeasibility**: the ability to withdraw or
replace a fact without erasing history. `drv-026` added exactly one primitive for
that — the `supersedes` edge — and a pure fold ([`internal/kb`](../internal/kb))
that projects a log into its set of currently-*live* facts, with the superseded
ones kept for history and reachable via `recall --chain`.

That one edge is the whole difference between a *record of what was said* and a
*model of what is believed*. A record accumulates; a belief store **revises**. Once
a fact could be retired by a later one, `recall` stopped being "read the log" and
became "read the current belief," and the door opened to everything below. The
belief store was not designed; it *emerged* from supersede. That is worth
remembering when weighing further generalization: the high-leverage primitives here
are small.

## What a belief store is

A belief store answers one question for a stateless consumer: **"What should I act
on as true about X right now, how much should I trust it, and why?"** It is not a
database (stores data), not a knowledge graph (stores relationships), not a log
(stores events). It stores **beliefs**: defeasible, provenanced, perspectival
assertions, plus the machinery to decide which are live and whose authority backs
them.

Designed greenfield rather than grown, its substrate is one atom — the
**assertion** `(claimant, time, subject, content, relations[])`, immutable once
written, hash-chained — and four choices from which the rest collapses out:

- **The claimant carries an authority rank**, not a free-form name. "Whose belief
  wins" is then *derived* from the rank order, not adjudicated case by case.
- **The subject is first-class and may itself be an assertion** — a belief can be
  *about another belief* ("I no longer trust observation #7") or *about a
  relationship* between two subjects. This is where edge/topology knowledge lives
  without imposing any taxonomy.
- **Relations are a tiny closed vocabulary.** Draiver has exactly one today
  (`supersede` = retract). The belief-store framing invites at most two more, and
  only on demand: **contradict-held-live** (two live beliefs conflict and *no one
  has adjudicated yet* — represent the dispute rather than force a premature
  winner) and **derive-from** (this belief was inferred from those, so it weakens
  when they are retracted — real truth maintenance). The rest (`corroborate`) can
  stay emergent. Four relations would be the over-engineering this project keeps
  refusing; the honest increment is these two.
- **A pure fold yields, per subject, the live set plus a *derived* epistemic
  status** — `canonical / corroborated / single-observation / disputed / stale` —
  computed from the relation graph, claimant rank, and commit-pin. Binary
  live/dead becomes a small status lattice. It is *derived, never stored*, so it
  cannot drift.

The payoff of that collapse: **escalation, review, resolution, and reconciliation
stop being distinct event types and become patterns over the one substrate.** An
escalation is a low-authority belief marked needs-adjudication. A resolution is a
high-authority assertion with a retract/decide relation. Reconciliation is a
reconciler-claimant asserting `derive-from` beliefs into a higher-authority
perspective, pending a human assertion above it.

### Perspectives, and the single primitive to protect

A **perspective** is a named parallel belief-set — a claimant's or role's own log.
Perspectives are *sovereign*: an agent writes only its own, so it can never stomp
another's, which is what dissolves the write-time trust problem structurally rather
than by policy. Perspectives **never auto-merge** (unlike git branches); a
higher-authority **canonical** perspective is reached only by *reconciliation* — an
agent proposes a synthesis, a human accepts it. Growth is therefore by *curation,
not convergence*, which is both the safety property and the throughput ceiling.

The one thing to protect at all costs is that **the fold interprets a fixed, small,
closed vocabulary and nothing else** — tags and content stay opaque to it. The
moment the fold reasons over open-ended content, it stops being auditable and
deterministic, and the whole trust story collapses. Reasoning belongs in the agent
layer *on top* of the store, exactly as CI and GitOps controllers reason on top of
a git that only stores and versions. **The belief store is a substrate for belief,
not a reasoning engine.**

## The two reconcilers

GitOps is *git + a reconciler*: a declarative desired state in git, and a control
loop (Flux/ArgoCD for k8s; Terraform + CI) that continuously drives actual state
toward it. Draiver is a belief store wired between **two** such loops, pointing in
opposite directions.

**The actuation loop — the GitOps-analog proper.** Desired state is the ticket
spec; actual state is the codebase; the controller is the coding agent, driving
code → spec through attempts/tests/review. The `ctl` reconcile loop and
tickets-as-desired-state (already noted in
[`coding-agent-platform.md`](./coding-agent-platform.md)) are this reconciler's
harness.

**The perception loop — the one GitOps does not have.** Here the arrow reverses:
observations from the world flow *into* the belief store and are reconciled into
canonical belief. This drives the *model* to track *reality*, not reality to track
the model. GitOps's git is write-by-human and never senses; adding this second loop
is precisely the generalization *beyond* GitOps.

The belief store sits between them: the perception loop writes it, the actuation
loop reads it. A coding agent's action is a function of *(desired spec, current
belief, actual code)* — the reconciler consults a **learned model** of the plant,
not just raw plant state. That learned model is the entire point: it is what lets
stateless actuators act competently across episodes instead of re-deriving the
system every time, which is the thing the project set out to capture — *a
resolution learned in one ticket sparing a re-escalation in the next.*

## The deep isomorphism

| GitOps / control theory | Draiver |
| --- | --- |
| Declarative desired state in git | Ticket spec (the setpoint / reference) |
| Plant — the cluster/infra being driven | The codebase / system |
| Sensor — reads actual state | Coding agent's observations (world → belief) |
| State estimate / model of the plant | The **belief store** (folded, supersedable, perspectival) |
| Actuator / controller — drives plant to setpoint | Coding agent (belief + spec → code) |
| Reconcile loop: observe → diff → act → repeat | `ctl` + attempts + review + land |
| Drift detection | Commit-pin staleness on a belief |
| Operator / SRE on call | The human: adjudicates the model (escalate/resolve), accepts actuation (review) |

Read top to bottom it is a **sense → model → act** loop: agents sense the codebase
and write observations; the fold turns observations into current belief; agents read
belief-plus-spec and act on the code; acting produces new observations, and the loop
closes. Draiver is a **supervised closed-loop control system** in which the belief
store is the state estimate and the human is the supervisor of that estimate.

## Where the rhyme breaks — and why the trust machinery is load-bearing

GitOps is stable because of two assumptions draiver **inverts**:

1. **Its controller is dumb and deterministic.** A k8s reconciler never "escalates
   because it doesn't understand" — it retries toward a known goal. Draiver's
   controller is a *smart, stochastic, fallible reasoner*, so the loop needs a
   human arbiter and a review gate that GitOps does not.
2. **Its state is declared and authoritative.** Draiver's state is *learned and
   contested* — accumulated from fallible observations across perspectives.

Inverting both is strictly harder to keep stable, and that difficulty is the reason
draiver needs machinery a controller never does: authority ranks, supersede,
held-contradiction, the human gate. **This is the formal version of the
"recursively confident and wrong" fear.** Closing the loop over a *learned* model
creates a self-confirmation risk: a corrupted state estimate makes the actuator act
on it, which produces observations consistent with that action, which reinforce the
corrupted estimate — control instability from a bad estimate.

Seen this way, the trust machinery is not ornament; it is **damping**. The human
adjudication gate, the contradict-held-live primitive, and the commit-pin are what
keep the closed loop from diverging. Every debate in this project about "who may
assert what" was really a debate about loop stability.

### The authority asymmetry is mandatory, not stylistic

The two loops must keep **distinct** authority models:

- **Actuation is cheap and reversible** — a PR, reviewed, revertible — so it needs
  no heavy gate. Agents write code and their own per-ticket observations freely.
- **A model update conditions *all future* actuation**, so writing *canonical*
  belief must be human-gated.

That is why per-ticket observations flow freely while reconciliation into the
canonical perspective is gated. The control framing says this asymmetry is required
for stability, not a matter of taste: you may let the actuator be fast and loose
precisely *because* the estimate it reads from is slow and guarded.

## It still handles coding agents on tickets — as the degenerate case

A good generalization makes the thing you started with its simplest configuration,
not a bolt-on. The Jira/coding-agent case is exactly the belief store with every
extra axis set to its trivial value:

| Belief-store concept | Coding-agent-on-a-ticket instance |
| --- | --- |
| Subject | The ticket |
| Claimant (low rank) | The coding agent |
| Assertion | A `gotcha` / `decision` / `note` logged during work |
| `retract` relation | `log --supersedes` |
| Low-authority belief marked needs-adjudication | An **escalation** |
| High-authority assertion that decides | The human **resolution** |
| Work-product belief awaiting acceptance | **Review** |
| Query-by-subject → live set + status | `brief` / `recall` |

With one claimant, a trivial authority order (agent < human), no live
contradictions (a conflict is resolved immediately by supersede), and no derivation
edges in use, the belief store **reduces exactly to today's ticket log**. Nothing
about the coding workflow has to change for the general shape to be true; the
general shape is what the coding workflow already is, with the dials turned down.

## What exists, and what is latent

Built today: the append-only hash-chained log, the `supersedes` edge, the pure fold
to live facts, `recall`/`brief` as progressive-reveal reads, escalate/review/resolve
as the authority gate, and the `ctl` actuation loop.

Latent — the pieces the belief-store framing *invites* but that should be pulled
only when a concrete use case pays for them: named **perspectives** beyond the
ticket (a codebase or subject ledger with a curated canonical perspective); a
**reconciliation** agent that promotes a ticket's observations into that canonical
perspective under human review (the perception loop, made explicit — the
GitOps-for-belief step); the **contradict-held-live** and **derive-from** relations;
and the **derived epistemic status** lattice. See the discussion of the perception
loop above for the reconciliation sketch.

The test of whether any of this is real is the same test the project has always
applied: ship the narrow version and watch whether the accumulated belief actually
stops the re-escalations. The belief store earns each generalization the same way
it earned supersede — by a concrete need, not a grand plan.
