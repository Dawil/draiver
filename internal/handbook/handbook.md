
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
`resolve`: the human answers escalations from the board on their own time (§6).
Assume you cannot see the human in real time — you communicate by writing events
they will read on the board, not by chatting.

## 1. Cold-start from the brief — once per session, at the start

The first thing every session does — whether you are the first agent on the
ticket, a resumed session, or a fresh one taking over after a previous agent ran
out of context — is load the full context:

```
draiver brief <TICKET>
```

Treat its output as your complete starting context: the spec, a bounded **index**
of the currently-live facts (each a `#seq type [tags] — headline` line), and any
open escalation you are inheriting. The brief shows headlines, not full bodies —
pull a fact's detail with `draiver recall <TICKET> <seq>` and its full supersession
history with `draiver recall <TICKET> --chain <seq>` (see §5). Superseded and
resolved history is off the index by design; it is still on the log for `recall`.
**Do not assume you remember anything the brief or a `recall` doesn't show.** If
what you can brief and recall isn't enough to resume the work, that's a signal the
log is leaking state — fix it by logging more, below.

Run it **once, at the start of your session.** Re-briefing mid-session is a
mistake: don't loop back to it after each event, and — critically — don't poll it
after you escalate to see whether an answer has landed (see §6). Once you're
working, you have the context; keep going.

## 2. Translate the acceptance criteria into features

The spec's authority over *what "done" means* lives in its **`## Acceptance
criteria`** section — plain-text statements of intent (e.g. *"user can log in and
click through to the dashboard"*), authored by whoever wrote the ticket. The brief
shows them to you as ordinary spec content; they are **not** pre-materialised into
runnable tests, and there is no checked-in feature store to copy from.

Turning that prose into executable acceptance tests is **your job**: author the
corresponding Gherkin **`.feature` files + step definitions / execution scripts in
the code repo**, on your branch, and commit them alongside the code they cover.
They run under the repo's BDD rung (so `draiver test` can prove them green, §7)
and, on merge, graduate into trunk as a permanent deterministic acceptance rung —
the control plane growing one ticket at a time.

Interpret the criteria faithfully. If a statement is ambiguous enough that you
would be *guessing* at what "done" means, that is a spec question for a human —
`escalate` it (§6) rather than encoding a guess. A spec with no acceptance-criteria
section has nothing to translate; carry on.

## 3. Log gotchas the moment they bite

When something surprises you — a broken assumption, a hidden constraint, a
workaround — record it immediately so no one rediscovers it:

```
draiver log <TICKET> --type gotcha "Stripe test keys only work in test mode; live webhooks 401 until the account is verified."
```

## 4. Log decision points with the alternatives

Every meaningful fork: capture what you chose, what you rejected, and why. This
is what makes a fresh agent able to continue your reasoning instead of relitigating it.

```
draiver log <TICKET> --type decision "Chose server-side pagination over client-side: result sets exceed 10k rows and the table must stay responsive."
```

Use `--type note` for context that is neither a gotcha nor a decision, and
`--artefact <path-under-artefacts/>` to reference a blob (a failing log, a
screenshot) instead of pasting it inline.

**Write log bodies in Markdown.** The board renders every event body as Markdown
(and sanitizes it — raw HTML and `javascript:`/`data:` links are stripped, so you
cannot break the page). Use that structure to make a log skimmable instead of a
wall of text: fence code and errors, wrap file paths and identifiers in
backticks, and list a decision's rejected alternatives as bullets. It stays plain
text on disk and in `brief`; only the board renders it, so a body with no Markdown
still reads fine. For example:

```
draiver log <TICKET> --type decision "$(cat <<'MD'
Chose **server-side** pagination over client-side.

- *Rejected* client-side: result sets exceed 10k rows; the table janks.
- *Rejected* cursor API: the backend only exposes `OFFSET`/`LIMIT` today.

Wired through `internal/api/list.go`; see the failing case in `pager_test.go`.
MD
)"
```

## 5. Supersede, recall, and tag — the log is a live index, not a transcript

The brief no longer replays the whole log. It pushes a bounded **index of live
facts** — one `#seq type [tags] — headline` line each — and omits everything a
later event has retired. That keeps cold-start cheap on a busy ticket, but it
changes both how you *write* the log and how you *read* it back.

**Write a self-contained first line.** The headline the index shows is the first
non-empty line of your body, stripped of Markdown markup and length-capped. Make
it stand on its own: `Chose server-side pagination — result sets exceed 10k rows`
reads in the index; `Here's what I decided:` does not. Put the detail in the lines
beneath it, and pull it with `recall` when a headline isn't enough.

**Supersede, don't duplicate.** When a fact turns out wrong or stale, do **not**
append a contradicting fact and leave both live. Find the old seq in the index and
retire it as you log the correction:

```
draiver log <TICKET> --type decision --supersedes 14 "Reverted to client-side pagination: the server caps the result set at 500, so 10k never happens."
```

The superseded fact leaves the brief's index — so a fresh agent never re-reads a
belief you already abandoned — but stays on the append-only log; `recall --chain`
walks the whole seemed-like-X → now-Y history. The log never mutates; liveness is
a projection over it. (`--supersedes` must name a seq that exists on this attempt.)

**Recall on demand.** `recall` is the read side of that index — read-only, offline,
no daemon, reading the same hash-chained log `audit` trusts:

```
draiver recall <TICKET>                 # the live index (seq, type, tags, headline)
draiver recall <TICKET> 14 22           # full bodies of #14 and #22, with artefact pointers
draiver recall <TICKET> --chain 14      # the supersession history of fact #14, oldest-first
draiver recall <TICKET> --type gotcha   # filter the index by type
draiver recall <TICKET> --tag auth      # filter by tag (repeatable; AND)
draiver recall <TICKET> --grep stripe   # filter by body/headline regexp
draiver recall <TICKET> --all           # include superseded facts in the index
```

Because it needs nothing but the on-disk log, `recall` is also how you **talk to an
attempt out of band**: a local Claude session (this skill + `recall`, no supervised
draiver run in the loop) can answer "what was decided about X", "known gotchas",
"the history of fact #N" by pulling from the log directly.

**Tags are yours.** `--tag` attaches opaque retrieval labels; draiver assigns them
no meaning — they never affect liveness or truth, only what `--tag` search returns.
Before inventing a tag, skim the tags already in the index and reuse one, so
`--tag auth` finds every related fact instead of three drifted spellings:

```
draiver log <TICKET> --type gotcha --tag auth --tag webhook "Stripe live webhooks 401 until the account is verified."
```

## 6. When you need a human, escalate — then stop

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

## 7. Verify with the test pyramid before you claim

If the repo ships a `.test-pyramid.yaml`, prove the work green before you hand it
off — and the order matters:

- **Iterate** with bare `draiver test`. It runs the pyramid from the base rung up,
  stops at the first non-green rung, and reports — nothing more. It tolerates a
  dirty tree, so it is your fast inner loop while you edit.

  ```
  draiver test           # climb to the top rung
  draiver test unit      # only up to the `unit` rung
  ```

- **Commit**, then run `draiver test --log`. With `--log`, draiver refuses a dirty
  tree, runs the pyramid, and **only on all-green** appends one durable
  `test-result` event naming the rung and the exact commit — a verdict draiver
  observed, not a self-report. A failing run records nothing, so the log holds
  only passing results. The next agent's `brief` inherits "rung green @ sha" for
  free.

A repo with no `.test-pyramid.yaml` has nothing to run: `draiver test` says so and
exits clean. Do this before §8 — a review backed by a logged green result is one a
human can trust at a glance.

## 8. Claim review — push, then claim with a link

When you believe the work is complete, make a **claim** for a human to verify.
Do not mark the ticket done; that decision is the human's. A claim the human
can't click through to is a claim they have to chase — so a review must carry a
**URL that points at the change**, and that means your branch has to be on a
remote first.

`draiver review` only appends the claim and validates the link — it does **not**
push or open a PR. You push, exactly as you run `git commit` yourself.

**a. Push your attempt branch to a remote.** Which remote:

- **Exactly one remote** (`git remote` prints one name) → push there.
- **More than one remote** → push to the **primary remote** named in the global
  config, `~/.draiver/config.json` key `primary_remote` (e.g.
  `"primary_remote": "forgejo"`).
- **Ambiguous** — several remotes with no `primary_remote` set, or a
  `primary_remote` that isn't in `git remote` — **do not guess a remote.**
  Escalate (§6): pushing to the wrong forge is worse than asking.

```
git push -u <remote> HEAD
```

**b. Derive the review URL from that remote.** Take the remote's URL
(`git remote get-url <remote>`), strip a trailing `.git`, and — for a web forge
(GitHub / Forgejo / GitLab) — build a **compare** URL against the main branch:

```
http://host:3000/owner/repo.git  →  http://host:3000/owner/repo/compare/main...<branch>
```

A compare URL needs no forge API and works across forges, so it is the default.
If you opened a real PR, link that instead.

**c. Claim, attaching the link.** `--url` defaults the link rel to `pr`; for a
branch-compare or diff use `--link compare=<url>` / `--link diff=<url>`:

```
draiver review <TICKET> "Pushed to forgejo; all tests green; covers the spec's three acceptance criteria." --link compare=http://host:3000/owner/repo/compare/main...drvctl-026-0001
```

or, when it's a real PR:

```
draiver review <TICKET> "PR #142 opened; all tests green; covers the spec's three acceptance criteria." --url http://host:3000/owner/repo/pulls/142
```

## Loop

Each session: `brief` once (an index of live facts; `recall` for detail) → work —
including translating the spec's acceptance criteria into Gherkin features + steps
on your branch — → `log` gotchas/decisions as you go, with self-contained first
lines, superseding stale facts rather than duplicating them → when blocked,
`escalate` and stop (the human resolves from their board, on their own time) → when
the ticket is worked again, a session `brief`s, reads the resolution, and continues
→ when the work is done, **verify** (iterate with `draiver test`, commit, then
`draiver test --log`), **push** to a remote, and `review` with a link the human can
click.

Everything valuable lives in the log. Leave the ticket resumable — the next
`brief` is the only handoff.
