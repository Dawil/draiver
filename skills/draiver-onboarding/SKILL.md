---
name: draiver-onboarding
description: >-
  Onboards a coding agent that has been handed a Draiver ticket. Use at the START
  of any session where you are assigned a ticket id (e.g. PROJ-123) and a data
  root is available via `draiver` on PATH or the DRAIVER_DATA env var. Teaches the
  agent to cold-start once from `brief`, externalize gotchas and decisions into
  the append-only log, ask humans ONLY via `draiver escalate` and record their
  answer with `draiver resolve`, and claim `review` rather than self-close.
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

Every human touchpoint goes through the CLI. **You** run the commands — never ask
the human to run `draiver` themselves. They talk to you in the conversation; you
translate that into log events.

## 1. Cold-start from the brief — once, when you pick up the ticket

The first thing you do on a ticket you did not start is load the full context:

```
draiver brief <TICKET>
```

Treat its output as the complete truth: the spec, every prior decision, and any
open escalation you are inheriting. **Do not assume you remember anything the
brief doesn't show.** If the brief isn't enough to resume the work, that's a
signal the log is leaking state — fix it by logging more, below.

This is a **one-time entry step.** It is NOT something you repeat during a
session — not after each event, and *not after an escalation is answered*. If you
are already working the ticket, you have the context; keep going. Re-briefing
mid-session is a mistake.

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

## 4. When you need a human, escalate — that is the ONLY way to ask

When you hit something only a human can decide (missing credentials, a product
choice, an ambiguous spec), **do not guess past it, and do not invent your own
way of asking** — no ad-hoc question list, no form, no "please answer these"
message. The single mechanism for putting a question to a human is
`draiver escalate`. If you didn't run `escalate`, you didn't ask.

**a. Capture the context first** so the escalation is self-contained — the human
should not have to ask you what you already know:

```
draiver log <TICKET> --type note "Deploy needs a prod DB URL. Tried the staging URL (works locally); prod is firewalled from CI. Blocked on the real value or a decision to mock."
```

**b. Escalate.** This appends the question and exits nonzero (3) to signal a
block. Stop working the blocked path, then surface the question to the human in
the conversation:

```
draiver escalate <TICKET> "What prod DB URL should CI use, or should this environment be mocked?"
```

**c. Record the human's answer yourself.** When the human answers you (in the
conversation), it is *your* responsibility to write it back with `draiver
resolve`, referencing the escalation's seq. Attribute it to the human, since it
is their decision:

```
draiver resolve <TICKET> <ESCALATION_SEQ> "Mock it: CI reads DATABASE_URL from a fixture; a follow-up ticket wires the real secret." --actor human:<name>
```

(You know the escalation's seq from the `escalate` output, or from `draiver
brief`. Never tell the human to run `resolve` — that's your job.)

**d. Act on the answer and log what you did** — no re-briefing, you already have
the context. Just continue, and record how you applied the resolution so the
escalate → resolve → action arc is one durable trail:

```
draiver log <TICKET> --type note "Applied resolution #7: CI now reads DATABASE_URL from a fixture; opened PROJ-140 for the real secret."
```

## 5. Claim review — don't self-close

When you believe the work is complete, make a **claim** for a human to verify.
Do not mark the ticket done; that decision is the human's.

```
draiver review <TICKET> "PR #142 opened; all tests green; covers the spec's three acceptance criteria."
```

## Loop

Cold-start once with `brief` → work → `log` gotchas/decisions as you go → when
blocked, `escalate` (the only way to ask) and stop the blocked path → record the
human's answer with `resolve` and continue *without re-briefing* → `review` when
done.

Everything valuable lives in the log. Leave the ticket resumable.
