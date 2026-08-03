---
name: draiver-onboarding
description: >-
  Onboards a coding agent that has been handed a Draiver ticket. Use at the START
  of any session where you are assigned a ticket id (e.g. PROJ-123) and a data
  root is available via `draiver` on PATH or the DRAIVER_DATA env var. Teaches the
  agent to cold-start each session from `brief`, externalize gotchas and
  decisions into the append-only log, ask humans via `draiver escalate` and then
  stop (the human resolves from their board — the agent does not run `resolve`),
  and claim `review` rather than self-close.
---

# Working a Draiver ticket

You are a **stateless** worker on one **attempt** of a ticket. A ticket may have
several attempts — different tools or models trying the same spec — but you only
ever work *yours*. Your in-flight context is **not precious**; the attempt's
durable log is. Another agent (or you, freshly respawned) must be able to resume
your attempt from disk alone. Your job is to keep that log good enough that they
can.

The task names your **ticket id** and **attempt id**, plus the data root. Export
both so every command targets your attempt and attributes your writes:

```
export DRAIVER_ACTOR=agent:<your-name>    # e.g. agent:claude-code
export DRAIVER_ATTEMPT=<attempt-id>        # e.g. 0001 — the attempt you were assigned
```

Without `DRAIVER_ATTEMPT` (or a `--attempt <id>` flag), commands fall back to the
ticket's *latest* attempt — which may not be yours, so set it. If the data root
isn't the default, pass `--data <dir>` or export `DRAIVER_DATA`.

Your side of the protocol is entirely CLI: `brief` to load, `log` to record,
`escalate` to ask, `review` to hand off. Run those yourself — never offload your
own logging onto the human. The one verb that is *theirs*, not yours, is
`resolve`: the human answers escalations from the board on their own time (§4).
Assume you cannot see the human in real time — you communicate by writing events
they will read on the board, not by chatting.

## 1. Cold-start from the brief — once per session, at the start

The first thing every session does — whether you are the first agent on the
ticket, a resumed session, or a fresh one taking over after a previous agent ran
out of context — is load the full context:

```
draiver brief <TICKET>
```

Treat its output as the complete truth: the spec, every prior decision, and any
resolved-or-open escalation you are inheriting. **Do not assume you remember
anything the brief doesn't show.** If the brief isn't enough to resume the work,
that's a signal the log is leaking state — fix it by logging more, below.

Run it **once, at the start of your session.** Re-briefing mid-session is a
mistake: don't loop back to it after each event, and — critically — don't poll it
after you escalate to see whether an answer has landed (see §4). Once you're
working, you have the context; keep going.

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

## 4. When you need a human, escalate — then stop

When you hit something only a human can decide (missing credentials, a product
choice, an ambiguous spec), **do not guess past it.** The one mechanism that
actually puts a question to a human is `draiver escalate`: it records the
question durably, and the human sees it on their board (`draiver webui`, under
**Needs me**). If you didn't run `escalate`, you didn't ask — a message in the
conversation reaches no one, because the human is watching the board, not the CLI.

A written-out list or form of questions is fine **as long as `escalate` ran
first** — the escalation is the durable record; the prose is just a nicer way to
read it. Put the real question in the `escalate` body.

**a. Capture the context first** so the escalation is self-contained — the human
should not have to ask you what you already know:

```
draiver log <TICKET> --type note "Deploy needs a prod DB URL. Tried the staging URL (works locally); prod is firewalled from CI. Blocked on the real value or a decision to mock."
```

**b. Escalate, then stop working this ticket.** `escalate` appends the question
and returns a nonzero exit (3) to signal a block. Your turn on this ticket ends
here. **Do not** poll, wait, or loop back to `brief` to check whether it's been
answered — the human resolves asynchronously, on their own time.

```
draiver escalate <TICKET> "What prod DB URL should CI use, or should this environment be mocked?"
```

**The human resolves it, not you.** Monitoring the board, they run `draiver
resolve <TICKET> <seq> "<answer>" --attempt <your-attempt>` themselves (the
`escalate` output prints the exact command). You never run `resolve`, and you
never tell them to run it — the board already routes them there.

**When the ticket is worked again** — whether you are resumed with fresh context
budget or an entirely new session takes over — it re-enters at step 1 with
`brief`, which now shows the resolution inline. *That* is when you act on the
answer and log what you did, closing the escalate → resolve → action arc:

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

Each session: `brief` once → work → `log` gotchas/decisions as you go → when
blocked, `escalate` and stop (the human resolves from their board, on their own
time) → when the ticket is worked again, a session `brief`s, reads the
resolution, and continues → `review` when the work is done.

Everything valuable lives in the log. Leave the ticket resumable — the next
`brief` is the only handoff.
