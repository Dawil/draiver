# Draiver is a Coding Agent Platform

> A Coding Agent Platform is **platform engineering pointed at a different scarce
> resource.** Platform engineering conserves the platform team's attention under a
> *compliance* constraint; draiver conserves the supervisor's attention under a
> *correctness* constraint. Same machine — self-service, management-by-exception,
> codify-the-judgment-once, audit everything — aimed at AI coding agents instead of
> cloud infrastructure. What makes it its own discipline, and not "PE for agents,"
> is the one place the analogy breaks: **PE's workers are deterministic; a coding
> agent's are not.**

The companion docs answer *what category of thing draiver is* from the inside —
[`control-plane.md`](./control-plane.md) ("a control plane whose scarce resource is
human attention"), [`draiverctl.md`](./draiverctl.md) (the systemd map). This
document places draiver against the **industry story it most rhymes with —
platform engineering** — and works out where the rhyme holds, where it breaks, and
what that says about how far the human can actually be removed.

## The industry story it rhymes with

Platform engineering begins when a cloud provider (AWS) starts offering raw
capability, and a regulated, low-trust enterprise cannot simply hand every
developer root. A **platform** is the mediating layer that lets developers
self-serve *safely*. On day 1 the platform is a human: a developer files "please
provision me a database," an admin does it by hand. Every day after is about
**reducing that human toil and raising automation and self-service — without
compromising, ideally improving, the governance.** The admin's per-request judgment
gets frozen into a **golden path**: a self-serve API with the compliance encoded in
the rails, so the human leaves the per-request loop and stays only for policy and
exceptions. The end-state is compliant Infrastructure-as-Software — an internal
cloud provider, fully self-serve, fully governed.

Draiver's development cycle has three stages:

1. **Scope** — create the ticket, define acceptance criteria.
2. **Build** — the agent codes the ticket.
3. **Ratify** — review, approve, merge; CI/CD takes over.

Stages 1 and 3 are the developer's job. **Stage 2 is where the new toil lives** —
the babysitting: loading context into agents, asking "do you have everything you
need," jumping to whichever agent is blocked. That babysitting is the exact analog
of the day-1 provisioning admin, and the coding-agent platform's arc is the exact
analog of PE's: **minimize the human in stage 2 without lowering the bar.**

## The deep isomorphism

Both platforms are, at bottom, one machine: **a converter that turns an expensive
one-time human judgment into a cheap, durable, replayable artifact.** They line up
row for row.

| Platform Engineering | Draiver |
| --- | --- |
| Day-1 admin provisions a DB by hand | Human babysits an agent through a ticket |
| Freeze it into a **golden path** — self-serve, policy encoded in the rails | Freeze the answer into the **durable log** — an escalation+resolution is "one artefact," read inline on resume, never re-litigated |
| Human stays for **policy exceptions**, not per-request | Human stays for **escalations + Review**, not for progress (`Running` is a count) |
| Audit trail / policy-as-code proves compliance | Hash-chained, tamper-evident log proves provenance & attribution, independent of git |
| IAM / least-privilege — can't give devs root | Tool-permission callback **is** the escalation seam: the agent's authority boundary becomes the human gate |
| Declarative reconciler (IaC → the operator pattern) | Tickets-as-desired-state, agents-as-cattle; the `ctl` reconcile loop |

The IAM row is the one to hold onto, because it is the reason the platform exists
at all. PE is born of a **low-trust boundary** — the enterprise cannot hand
developers root. Draiver has the identical primitive already built: the untrusted
principal is the *agent*, `"may I?"` routes to `escalate` instead of
auto-approving, and the maturation path is IAM-policy maturation exactly — gate
everything at first (high toil, high safety), then codify auto-approve policies for
the safe classes, keeping the human only for genuinely privileged asks. Draiver is
not *analogous* to an IAM; it is building one.

## The one place the analogy breaks — and it is load-bearing

**PE's leaves are deterministic. A coding agent's are not.** `terraform apply` does
not drift, loop, or wander off-task; a coding agent does all three. Two
consequences follow that have **no PE analog**, and both are already named in the
companion docs:

1. **Progress ≠ liveness** ([`draiverctl.md`](./draiverctl.md)). PE never had to ask
   whether a worker was still moving *toward intent* — only whether it was alive.
   Intent-supervision — loop and thrash detection, a judge over the stream — is a
   coding-agent-native subsystem with no systemd or Terraform equivalent.
2. **Success is a claim, not exit 0.** `terraform apply` exit 0 is ground truth: the
   resource exists and is compliant, verifiably. An agent's `review` is
   "`sd_notify READY=1` that a human must countersign." PE can trust the worker's
   own success signal. **A coding-agent platform structurally cannot.**

This is why "stages 1 and 3 are out of scope" is *true today for a deep reason*, not
a boundary of convenience: **the platform's trust model cannot extend to the
worker's self-report.** PE removed the human because the machine became *reliable*.
Draiver removes the human where it can but must keep a supervisory loop *because the
machine is unreliable by nature*. The design answer is
[`control-plane.md`](./control-plane.md)'s **deterministic spine, stochastic
leaves**: the governance layer holds no model, so non-determinism is quarantined in
the workers where it can be watched, judged, and reaped. It is the principle PE
never needed, and the one to defend hardest against the seductive "just make the
supervisor an agent too" — which would give you stochastic-all-the-way-down.

## Core values of a Coding Agent Platform

The first four draiver shares with platform engineering. The last four are native —
forced by the stochastic leaf.

1. **Conserve human attention, not compute.** The `Needs me` queue is the human's
   run-queue; everything schedules to keep it short. (PE conserves the platform
   team; this conserves the supervisor.)
2. **Every human judgment becomes a durable, attributed, replayable artifact.**
   (= PE's "codify once," plus a compliance/provenance substrate for free.)
3. **Management by exception.** The human attends to escalations and reviews, and to
   nothing that is merely progressing.
4. **Least-privilege workers; privilege requests are the human gate.** (= IAM.)
5. **Deterministic spine, stochastic leaves.** The governance layer is model-free
   and reproducible; non-determinism lives only in the workers. *The principle PE
   never needed, and the one that bounds the whole design.*
6. **Success is ratified, never self-certified.** Independent verification — human,
   tests, judge, tournament — is structural, not optional. *Stricter than PE, which
   can trust `apply` exit 0.*
7. **Externalize all context; the worker is disposable.** Durable context is the
   single precondition for self-service, audit, *and* comparison at once.
8. **Attempts compete.** Run N, `pick` the winner — non-determinism turned into an
   asset. PE has no analog: you do not run three Terraforms and choose one.

## Minimizing the stage-2 human: the flywheel

The natural next thought — an agent that handles simple escalations, the way AWS
Support has tiers — is right, but only once escalations are split correctly.

**Escalations are not uniform, and the split is exactly the PE line.**

- **Policy-lookup escalations** — the answer already exists, in a prior resolution,
  the spec, or a paved convention ("which base image?"). AWS Support L1 territory.
- **Novel-judgment escalations** — a first-of-kind decision, or a cross-ticket
  contract (the "pass the ball" of [`targets-and-dependencies.md`](./targets-and-dependencies.md),
  which insists — correctly — that the human stays the arbiter).

PE-style automation applies **cleanly and only to the policy-lookup class.** L1
answers "how do I resize this"; it escalates "should we architect it this way." So
the path to a smaller stage-2 human is the **PE toil-reduction flywheel, and
draiver's durable log is the demand signal that drives it:**

> Every escalation that *recurs across tickets* is a judgment the human keeps
> re-answering — the same signal as "everyone keeps asking for a Postgres." You
> mine the escalation log for those recurrences and promote each answer *out* of
> the per-ticket human loop and *into* a golden path: the onboarding skill, the
> spec template, or an auto-resolver.

Management-by-exception generates the very dataset that lets you shrink the set of
exceptions. That is the engine. It respects the spine/leaf rule too: an agent may
*retrieve* a policy-class answer, but the moment an agent *authors* a novel
resolution you have put a model back into the spine — so an agent-drafted resolution
must itself be subject to Review, or restricted to escalations a cheap independent
check can self-verify ("try the base image; tests pass").

## The two asymptotes differ — the honest bottom line

Platform engineering's end-state removes the human from the per-request loop
*entirely* — present only to author policy. A Coding Agent Platform's asymptote is
**structurally more bounded.** The human can be removed from progress, from
permission-policy, and from recurring policy-class escalations — but the
**ratification gate** (Review → Done) and **novel-judgment escalations** are
irreducible, *until* the independent verifiers (CI, judge agents, the tournament)
become good enough to countersign — at which point you have **moved** the trust
boundary, not deleted it. The residual human core is larger and more permanent than
PE's, and its size is set entirely by how good your verifiers get.

**But the boundary moves — that is PE's central lesson, and it applies here too.**
What is inherently-human at platform maturity *N* gets paved at *N+1*. Draiver
already shows stage 3 creeping into scope: `ctl merge` records `done` = *the code is
in the base*, `ExecStopPost` opens the PR on `review`, `race`/`pick` selects a
winner. The *mechanical* half of stage 3 is already absorbed; only the *judgment* of
approval remains. Stage 1 is where the flywheel feeds back — the spec template
improves from what agents kept getting stuck on. So the truer statement is not "the
platform owns stage 2 and stages 1 and 3 are out of scope," but:

> The human's residual role in **all three** stages is the *judgment*, and the
> platform's arc is to shrink the judgment surface everywhere by codifying what
> recurs. Stage-2 babysitting is simply the most acute toil today — which is why it
> is the right beachhead, the same way provisioning was PE's.

## The compression

> A **Coding Agent Platform** is the self-service, audited,
> management-by-exception discipline of platform engineering, applied to AI coding
> agents — with one structural difference that reshapes everything downstream: its
> workers are **stochastic**, so success is a claim to be ratified rather than an
> exit code to be trusted, and the human core it converges on is irreducibly larger
> than a cloud platform's.

Three names for it, from three altitudes:

- **Mechanism:** a control plane that conserves human attention instead of compute
  (see [`control-plane.md`](./control-plane.md)).
- **Discipline:** platform engineering's flywheel — mine the recurring exception,
  pave it into a golden path — running over a durable escalation log.
- **Constraint:** a deterministic governance spine wrapped around stochastic
  leaves, because the worker cannot be trusted to certify its own success.
