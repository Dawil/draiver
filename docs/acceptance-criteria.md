# Acceptance criteria — authored as prose, realised as features

> Acceptance criteria are the **human's authority over what "done" means.** This
> document fixes *where they live* and *who turns them into runnable tests* —
> deliberately **without** committing to a feature data-model. They are authored as
> **plain text in `spec.md`**; the attempt's agent translates that prose into
> Gherkin `.feature` files + steps **in the code repo**. Nothing is materialised
> into the data root, and there is no checked-in feature store.

## Where the criteria live

Acceptance criteria are **plain text in `spec.md`**, under a recommended
`## Acceptance criteria` heading. That heading is a light **convention**, not a
schema: there is no new frontmatter field and no validator. `draiver new`
scaffolds the section into every fresh spec (see `cmd/new.go`), so the prompt is
there for whoever writes the ticket — human *or* agent — to fill with statements of
intent, e.g.:

```markdown
## Acceptance criteria

- A user can log in with valid credentials and lands on the dashboard.
- Invalid credentials show an inline error and do not navigate.
```

Because the criteria are ordinary spec prose, **`brief` surfaces them for free** —
it already replays the whole `spec.md` body into the cold-start blob, so a fresh
agent sees the criteria with no new machinery.

## The flow: authored prose → agent-authored features

1. **Author** the criteria as prose in `spec.md` (human or ticket-writing agent).
   That is the entire authored surface.
2. Once an attempt begins, it is **on the agent** to translate that prose into
   proper Gherkin **`.feature` files + step definitions / execution scripts in the
   code repo**, on its branch, committed alongside the code they cover. The
   onboarding protocol directs the agent to do this (see
   [`../internal/handbook/handbook.md`](../internal/handbook/handbook.md) §2, kept
   byte-identical with the onboarding skill).
3. Those features **run under the repo's BDD rung** of the
   [test pyramid](./test-pyramid.md), so `draiver test` can prove them green before
   the attempt claims Review.
4. On merge they **graduate into trunk** as a permanent deterministic acceptance
   rung — the control plane growing one ticket at a time.

The arc is the platform flywheel in miniature: a human's one-time statement of
intent becomes a durable, replayable verifier
([`coding-agent-platform.md`](./coding-agent-platform.md), stage 1 → stage 2).

## Why this shape — the accepted trade-off

Making the agent do the prose → Gherkin translation **introduces flakiness**: the
agent may interpret the criteria imperfectly, so the features it writes may not
perfectly capture the intent. **We accept that on purpose.** The alternative — a
checked-in, ticket-owned `features/` store with a materialisation step into each
worktree — commits us to a feature data-model *before we know we need one*, and a
bad model is far more expensive to undo than a round of imperfect translation.

Keeping the authored surface as plain text **leaves the design door open.** If a
richer ticket-owned feature store later proves worthwhile, it can be added without
undoing any of this: the plain-text criteria remain the source of intent either
way, and the agent-authored features remain the executable realisation.

## Out of scope (deliberately not built here)

- A checked-in, ticket-owned `features/` store and materialisation into worktrees.
- Running the features, capturing their results, or rendering them — those belong
  to the BDD rung and its reporting work, filed separately.
