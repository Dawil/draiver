---
name: draiver-onboarding
description: >-
  Onboards a coding agent that has been handed a Draiver ticket. Use at the START
  of any session where you are assigned a ticket id (e.g. PROJ-123) and a data
  root is available via `draiver` on PATH or the DRAIVER_DATA env var. Teaches the
  agent to cold-start from `brief`, externalize gotchas and decisions into the
  append-only log, treat escalation as a hard halt, and claim `review` rather than
  self-close.
---

# Working a Draiver ticket

You are a **stateless** worker on a ticket. Your in-flight context is **not
precious** — the ticket's durable log is. Another agent (or you, freshly
respawned) must be able to resume from disk alone. Your job is to keep that log
good enough that they can.

The ticket id and data root come from the task. All commands take the ticket id;
set `DRAIVER_ACTOR=agent:<your-name>` (e.g. `agent:claude-code`) so your writes
are attributed. If a data root isn't the default, pass `--data <dir>` or export
`DRAIVER_DATA`.

## 1. Cold-start from the brief — always first

Before touching code, load the full context:

```
draiver brief <TICKET>
```

Treat its output as the complete truth: the spec, every prior decision, and any
open escalation you are inheriting. **Do not assume you remember anything the
brief doesn't show.** If the brief isn't enough to resume the work, that's a
signal the log is leaking state — fix it by logging more, below.

## 2. Log gotchas the moment they bite

When something surprises you — a broken assumption, a hidden constraint, a
workaround — record it immediately so no one rediscovers it:

```
draiver log <TICKET> --type gotcha "Stripe test keys only work in test mode; live webhooks 401 until the account is verified."
```

## 3. Log decision points with the alternatives

Every meaningful fork: capture what you chose, what you rejected, and why. This
is what makes a fresh agent able to continue your reasoning instead of relitigating it.

```
draiver log <TICKET> --type decision "Chose server-side pagination over client-side: result sets exceed 10k rows and the table must stay responsive."
```

Use `--type note` for context that is neither a gotcha nor a decision, and
`--artefact <path-under-artefacts/>` to reference a blob (a failing log, a
screenshot) instead of pasting it inline.

## 4. Escalation is a hard gate — capture context *before*, act *after*

When you hit something only a human can decide (missing credentials, a product
choice, an ambiguous spec), **do not guess past it.**

**Before escalating**, write the surrounding context so the escalation is
self-contained — the human should not have to ask you what you already know:

```
draiver log <TICKET> --type note "Deploy needs a prod DB URL. Tried the staging URL (works locally); prod is firewalled from CI. Blocked on the real value or a decision to mock."
```

**Then escalate.** This appends the question and **halts with a nonzero exit
(3)**. Stop working the ticket — the gate is enforced by process control, not by
your goodwill:

```
draiver escalate <TICKET> "What prod DB URL should CI use, or should this environment be mocked?"
```

**After** a human resolves it, you'll see the answer inline in `draiver brief`
when you resume. Record how you acted on it so the escalate → resolve → action
arc is one durable trail:

```
draiver log <TICKET> --type note "Applied resolution #7: CI now reads DATABASE_URL from the prod secret; migration ran clean."
```

## 5. Claim review — don't self-close

When you believe the work is complete, make a **claim** for a human to verify.
Do not mark the ticket done; that decision is the human's.

```
draiver review <TICKET> "PR #142 opened; all tests green; covers the spec's three acceptance criteria."
```

## Loop

`brief` → work → `log` gotchas/decisions as you go → `escalate` and **halt** when
blocked → resume after a resolution and `log` what you did → `review` when done.

Everything valuable lives in the log. Leave the ticket resumable.
