# What draiverctl actually is — the concept beneath the systemd analogy

> The MVP made agent *memory* into cattle: the log is the pet, the session is
> disposable. `draiverctl` applies the same move one level up and makes agent
> *supervision* into cattle. Management became a restartable process over the
> log, the way memory already had. That recursion — not the systemd rebrand — is
> the event.

`docs/draiverctl.md` explains `ctl` through systemd, exhaustively and correctly.
This document answers a different, meta question: **what *category* of thing did
adding `ctl` create?** The systemd table tells you what it resembles; this tells
you what it *is*, and rules out the near-miss framings that sound right.

## The one-sentence claim

`ctl` split a **control plane** from a **data plane**, and gave that control
plane an objective function no prior control plane has: **minimize the draw on
human attention**, not on compute.

- The draiver MVP is a **data plane**: the append-only, hash-chained log is the
  source of truth, and everything else (`brief`, `status`, the board) is a *read*
  projection over it. It can *describe* the fleet.
- `ctl` adds the thing that *acts*: a reconciler that continuously diffs desired
  state (which tickets should have a live, in-budget, progressing session)
  against actual state (what is running) and closes the gap. It *drives* the
  fleet.

That is why a supervising process feels categorically different from managing
sessions by hand. You did not build a better remote control. You inserted an
autonomous agent-of-a-different-kind between yourself and the workers, and the
management loop stopped being a thing you *do* and became a thing that *runs*.

## Three near-miss framings, adjudicated

Each touches a real edge of the thing and then mislocates it.

### "Agents as code / Infrastructure as Code"

Right family, wrong member — on two counts.

1. **Convergent vs continuous.** IaC (Terraform) is declarative but *convergent*:
   plan, apply, done. `ctl` is the *continuous* form — a controller that never
   stops reconciling. That is the Kubernetes **operator pattern**, which the
   locked decisions already name ("Declarative reconciler, not imperative…
   k8s-flavoured systemd").
2. **The declared unit is not the agent.** "Agents as code" implies you are
   version-controlling agent definitions. You are not. The **ticket** is the
   declared desired state ("this should reach a merged PR"); agents are fungible
   compute reconciled toward it. The right phrasing is
   *tickets-as-desired-state, agents-as-cattle.*

### "Moving human↔agent interaction to async"

True, but it predates `ctl`. `escalate`/`resolve` + the board is *already* the
async substrate: the agent escalates, its turn ends, the human answers from the
board. What `ctl` adds is *closing that async loop without you* —
path-activation wakes a `--resume` session on a `resolution` event, so the human
is no longer even on the return path of the async call. The async did not arrive
with `ctl`; what arrived is that **you were removed from the return path**. You
went from synchronous babysitter to an interrupt handler on a run-queue.

### "Higher-order agents — agents that create agents"

The one to drop, because it hides the load-bearing distinction. `draiverctld`
**is not an agent**: it is a deterministic control loop with no model in it. It
spawns agents but does not *reason*, so it is not a function returning a function
(same kind → same kind). It is a *different kind* supervising the stochastic
kind — a kernel scheduling processes, not a higher-order function.

That asymmetry is the design. A supervisor that were *itself* an agent gives you
stochastic-all-the-way-down, drifting at every level. `ctl` is the opposite: a
**deterministic spine** so non-determinism is quarantined in the leaves, where
it can be watched, judged, and reaped.

"Higher-order agents" is the *near* miss precisely because it correctly senses a
**level jump** and then wrongly assumes the new level is the same kind of thing
as the level below. It isn't.

## The word that was escaping us: reification, applied recursively

The concept is **reification** — turning supervision from a tacit human activity
into an explicit, running, manipulable artifact. But the design-specific way to
say it is sharper:

**You applied the cattle principle recursively, one level up.**

| Level | What was externalized | Onto what | Consequence |
| --- | --- | --- | --- |
| MVP | agent **memory** | the log | agents become stateless; the memory is the pet |
| `ctl` | agent **supervision** | the reconciler | management becomes stateless; the log is still the pet |

The decision is explicit: `draiverctld` "holds no precious state… rebuildable
from the log — `draiverctld` is cattle too." The MVP's throughline —
*externalize everything valuable so your own context is never precious* — now
includes the **supervisor's** context. Management became cattle. You are now two
levels removed: you supervise the thing that supervises, and both levels below
you are fungible and reconstructible from one durable log.

That recursion is the qualitative jump. Not "a better tool for managing agents,"
but "the act of managing has been externalized into a restartable process, the
way memory already was."

## Why it is more than a control plane

A classic control plane (systemd, k8s) schedules against CPU/mem/IO and asks
only *"is it alive?"* `ctl` cannot stop there, on two axes the AI-native section
of `docs/draiverctl.md` already names:

1. **Progress ≠ liveness.** Processes don't drift; agents do. The watchdog
   supervises toward *intent* ("is it moving toward the ticket"), not aliveness —
   a subsystem with no systemd analog.
2. **The scarce resource is human attention, not compute.** The scheduler's
   objective is *minimize enqueues to "Needs me."* Terraform conserves toil; k8s
   conserves compute; **this conserves human supervisory attention**, and treats
   it as the constrained resource everything else schedules around.

That second axis is the actual invention, more than the systemd resemblance.

## The compression

> `draiverctl` is a **control plane for agentic work whose scarce resource is
> human attention rather than compute** — and, like the log before it, the
> supervisor is stateless and log-reconstructible, so **management is as
> disposable as the agents it manages.**

Three names for the same thing, from three altitudes:

- **Mechanism:** a reconciliation loop (the operator pattern) — control plane
  over data plane.
- **Move:** reification of supervision — the cattle principle applied to the
  manager, recursively.
- **Novelty:** an attention-minimizing scheduler — the first control plane whose
  objective function is conserving a *human*.
