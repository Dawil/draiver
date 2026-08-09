# Targets & dependencies

> A companion to [`draiverctl.md`](./draiverctl.md). Where that doc maps the whole
> systemd model onto the supervisor, this one drills into two rows of its analogy
> table — **Targets (runlevels) → milestones/epics** and **`Requires=`/`After=`/
> `Wants=` → the ticket dependency DAG** — and works out how a *parent target
> ticket* and a runtime *"pass the ball"* handoff between siblings would actually
> sit in draiver's model. Nothing here is built; it is a design note to settle the
> forks before a Tier-2 ticket is written.

## The idea

A **target** is a parent ticket that is "reached" only when a set of child
tickets are complete — e.g. a release that needs both a **code repo** ticket and
an **infra repo** ticket landed. You `enable` the target, and its children join
the supervised fleet. While they run, a child that gets **stuck during testing**
because a *sibling* hasn't delivered what it needs can **pass the ball** to that
sibling — route its block there rather than sit idle. Passing the ball is an
**escalation requiring human approval**, not an automatic agent-to-agent handoff.

## How much of this is already designed

Most of the static half. `docs/draiverctl.md`'s analogy table already reserves
the slots, and the implementation order files them under **Tier 2 — fleet**
(*"Dependency DAG gating admission; … milestones/targets, `mask`, `focus`"*):

| systemd | draiver | doc's gloss |
| --- | --- | --- |
| Targets (runlevels) | **milestones / epics** | "Bring up all tickets for release X; reached when constituents are Done" |
| `Requires=` / `After=` / `Wants=` | **ticket dependency DAG** | "Don't spawn B until A is Done/merged. Gate admission on deps." |
| `isolate <target>` | **focus mode** | "Run only this milestone; pause the rest" |
| Socket / path activation | **resolution activation** | "The log dir *is* a `.path` unit: a new `resolution` event fires a resume session" |

So "enable a parent target → it pulls its children into the desired set → the
target is Done when its children are Done" is not new ground: it is the on-roadmap
systemd `.target`. It fits cleanly because a target is just another **ticket** — a
pet that contains pets — which keeps the "ticket is the atomic, portable unit"
invariant intact (team/project/assignee are fields, not folders; a dependency
edge is one more field).

Two foundations are already laid, which is why this is more ready than it looks:

- **Multi-repo is already real.** Repo binding is per-ticket (drvctl-015) and one
  controller drives attempts across many repos on demand (`managerFor` in
  `internal/reconcile/reconcile.go`). "Code repo + infra repo" is *literally two
  tickets with different `repo:` fields today* — the target is only the missing
  coordination layer over an already-multi-repo substrate.
- **"Child is Done" is already crisp.** `ctl merge` records `done` = *the code is
  in the base branch* (the terminal of the lifecycle `ctl` owns). A rollup
  ("target reached when children merged") keys on an existing, precise signal — the
  completion event does not have to be invented.

## The genuinely new part: passing the ball

The doc's DAG is **static admission gating** — a human declares up front "don't
start B until A is merged." Passing the ball is richer: a child, *at test time*,
**discovers** it is blocked on a sibling and asks for the block to be routed
there. That is a **runtime-discovered edge**, not a declared one.

The good news: it needs **no new primitive**. It is an escalation whose *resolver
is a sibling's milestone* instead of a human's keystroke. Traced through parts
that already exist or are already scoped:

1. Child **A** escalates: "blocked — need X from B" → A halts (Needs-me), durably
   logged. (Existing `escalate` → nonzero exit → board surfaces it.)
2. The human **approves the ball-pass** → this binds an edge **A → B** (and
   enables/nudges B if it is not already running). The human is the arbiter of the
   cross-ticket contract.
3. B does its work and `ctl merge`s → its `done` becomes the **resolution** of A's
   escalation, which **path-activates** A's resume (the doc's "a new `resolution`
   fires a resume session").

So **ball-passing = escalate → resolve → resolution-activation** (both Tier-1
primitives) **+ the Tier-2 DAG**, with exactly **one** new idea: *a resolution
sourced from a sibling's state transition rather than a human's text.* A small,
coherent addition — not a subsystem.

A nice property falls out: you **declare the edges you know** (infra before code)
and **discover the rest at test time**. The dependency graph is partly authored,
partly grown — and the two halves live in **different stores** (decision #3): the
declarations in the unit file, the discoveries in the log.

## Design decisions to settle

### 1. Keep human approval on the ball-pass — it is on-thesis, not just cautious

Draiver's premise is management-by-exception: the human attends to escalations and
reviews, nothing else. A cross-ticket contract ("A now depends on B delivering X")
is *precisely* a management decision. Auto-routing it is how you get two agents
ping-ponging a contract forever. Routing it as an escalation puts it on the exact
surface the human already watches — so this **strengthens** the thesis by making
hidden cross-repo coordination visible as first-class escalations rather than
buried agent chatter.

### 2. Decide the target's shape — the one place the flat model bends

Every ticket today is defined by its progression toward a PR: it has a repo, an
attempt, a worktree, and the daemon *tries to bring it up* (recall drvctl-017
escalates a repo-less attempt rather than idling it). A target has two possible
shapes:

- **Pure aggregator** — no repo, no agent, no worktree; its state is *rolled up*
  from its children (Done when all children Done). Cleanest `.target` analogy, but
  it is a genuinely **new ticket kind** the reconciler must learn to *never* spawn
  and to project state onto from elsewhere. It breaks the "state is derived from
  the ticket's *own* log" rule (the rollup is a function of *children's* logs).
- **Parent-with-integration-work** — the target carries the end-to-end test that
  only passes once both repos are wired. Then it is a **normal ticket** (repo =
  the integration/e2e repo) with dependency edges down to its children, and
  "reached when children Done" is just the static DAG gating *its own* admission.

The motivating phrase — *"when testing they get stuck"* — hints the integration
test **is** the coordination point, which favors **parent-with-e2e**. Lean that
way: it needs no new ticket kind, and ball-passing falls out naturally (the
parent's e2e is what discovers "infra isn't delivering X" and passes the ball
down). **This fork should be pinned before anything else is built.**

### 3. Where the edges live — the unit file gains its first relational field

`spec.md` + `attempt.md` already **are** the unit file (`draiverctl.md`'s analogy
table) — but only its identity + `ExecStart=` core: `attempt.Meta`
(`internal/attempt/attempt.go`) carries `tool`/`model`/`repo`/`base`/`from` and
nothing **relational**. A systemd unit is that *plus* directives the manager
reads — `Requires=`/`After=`/`Wants=` — and those have **no home in draiver
today**. A dependency edge is the first one that wants one. So "a dependency edge
is one more field" (above) is right; the real decision is *which store* the field
lives in, because draiver has **three**, not two:

1. **Static declaration** — `spec.md` / `attempt.md` frontmatter (the unit file).
2. **Durable semantic events** — the hash-chained `log/` (replayed by `brief`).
3. **Rebuildable runtime state** — `session/` (never in the hash chain).

Triage the DAG by tier and its two halves separate cleanly:

- **Authored edges** ("infra before code") are an immutable property of the
  *ticket's design*, shared across attempts — which is precisely what `spec.md`
  is (`store.go`: *"the immutable design input for a ticket, shared across
  attempts"*). So a declared edge belongs in **`spec.md` frontmatter**, not
  `attempt.md`: it is a ticket-level `Requires=`, not a per-journey fact.
- **Ball-pass edges** are *discovered at test time*, per-attempt, and born from an
  escalation. They belong in the **`log/`** as escalate→resolve, exactly where
  *"passing the ball"* already puts them — and they must **never** be promoted
  into frontmatter. A runtime-discovered contract written into the unit file is
  the precise mistake the static/dynamic split exists to prevent.
- **Rollup / "target reached" state** is *derived* from children's logs, so it is
  stored **nowhere** — computed on read. This is the sharp edge of decision #2's
  aggregator option: an aggregator's state is a function of *other* tickets' logs,
  which is why it strains the "state is derived from the ticket's *own* log" rule
  and why parent-with-e2e (whose state is its own) is the cleaner call.

The precedent that settles any "but declarative state could be a log event too"
worry: **`enable`/`disable` are log events, not frontmatter** — because they
*mutate over time*. That gives the operative rule — **immutable declaration →
frontmatter; mutable declaration → log; derived → nowhere.** An authored
dependency is immutable design input (frontmatter); a ball-pass is a mutation
(log); a target's reached-ness is derived (nowhere). drvctl-027's health log is
the same discipline one tier down — transient machine health goes to tier 3
(`session/ctl.jsonl`), never the durable log and never ticket control state — a
worked, already-shipping example of keeping dynamic signal out of the stores
reserved for something else.

### 4. Refuse cycles at the approval seam

Runtime edges mean **A → B** and **B → A** is a mutual-wait deadlock: both parked
forever, each waiting on the other. Human approval is a soft circuit-breaker (a
human should not approve a cycle), but the edge-approval path should **detect and
refuse** a cycle rather than trust vigilance. Cheap to check, nasty to debug if
skipped.

### 5. Don't build the scheduler before you need it

The **data model** here — a target ticket + dependency edges + a rollup — is cheap
and native to draiver. The **scheduler** — admission ordering, resolution-from-a-
sibling, focus/isolate, cycle detection — is the actual Tier-2 cost, and it is
exactly the "no scheduler" line Tier 0 deliberately drew (`reconcile.go`: *"Tier 0:
one adapter, admit-on-enabled, no scheduler"*). Let the elegance of the data model
not hide the scheduler cost.

A lightweight first cut, honest to how draiver has grown (enable/disable shipped at
Tier 0; the DAG waited): lean on the existing **`project`** field as the grouping,
make a designated epic ticket whose `spec.md` lists its children, and let
cross-ticket blocks be **prose escalations a human routes by hand**. Formalize
edges + rollup + auto-resume only once that manual toil is actually felt.

## Where it lands relative to draiver's thesis

Nothing here fights the model:

- **Agents are cattle, tickets are pets.** A target is a ticket — a pet that
  contains pets. Consistent.
- **Externalize context so no agent's context is precious.** Ball-passing is a
  cross-ticket handoff *mediated through the durable log and a human*, so the
  context stays externalized, never trapped in an agent.
- **Manage tickets, not agents; attend only to escalations and reviews.**
  Ball-pass *is* an escalation, so it routes to exactly the human surface draiver
  already defines.

The real design work is the two forks above — **aggregator vs. parent-with-e2e**,
and **how much scheduler you actually build** — not the concept, which the roadmap
already anticipates.

## Sketch of a build order (if/when it earns a ticket)

1. **Grouping + transitive enable.** A target ticket with `children:` edges;
   `enable`/`disable` on the target flows desired-ness down the DAG. Extends the
   existing `desired()` (which already composes the enable bit with an imperative
   marker) with a third path: enabled-via-parent. No child state is mutated.
2. **Rollup status.** Target's control state derived from its children's
   (Done when all children `done` = merged). Forces decision #2 (the target's
   shape). Additive projection.
3. **Static ordering (`After=`).** Gate a child's admission on a sibling's `done`.
   This is the first real *scheduler* increment.
4. **Ball-pass = cross-ticket resolution.** The novel bit: a human-approved
   escalation on A binds to B's milestone; B's `done` path-activates A's resume.
   Reuses escalate/resolve + resolution-activation; adds cycle detection at the
   approval seam.
5. **`focus` / `isolate`.** Run only one target, park the rest — budget/concurrency
   focus over the fleet.
