# The belief store: `type` carries identity, `state` carries what's happening

drv-037 is the foundation of the codebase-ledger epic (drv-035). It adds one
field to a ticket's `spec.md` frontmatter — `type` — and with it a second kind of
ticket. This note explains the distinction the field draws, because it is easy to
reach for `state` when you mean `type`, and the two axes are orthogonal.

## Two axes

- **`type` is identity — what a ticket *is*.** It is authored once, in the
  immutable `spec.md`, and never changes over the ticket's life. There are two
  values:
  - `ticket` (the default) — an ordinary **unit of work**. Every ticket written
    before drv-037 carries no `type:` line and so *is* a `ticket`; nothing about
    it changes.
  - `codebase` — a belief **ledger** for a codebase. Not a unit of work: a
    standing record of what is believed true about a body of code, accruing
    indefinitely.
- **`state` is what's happening — the human's relationship to the ticket *now*.**
  It is *derived* from the append-only log (`project.Derive` + the type-aware
  `restingState`), never authored, and it moves over time: Running, Pending
  (Parked), Needs-me, Review, Done.

A `codebase` ledger at rest is `Parked` + `type: codebase` — a *standing ledger*:
its identity says "I am a body of belief," its state says "nothing needs anyone
right now."

## Why a ledger behaves differently

A ledger has no setpoint and no actuation loop — there is no "done" to converge
on, only belief that accrues. So the `type` changes three behaviours, each a
consequence of "a ledger is not a unit of work," not a special case bolted on:

1. **Never actuates.** A ledger is never enabled and the `ctl` reconciler never
   spawns a session for it. This is enforced *by construction*, not by a gate:
   the reconciler admits precisely the attempts that are log-`Running` **and**
   enabled, and a ledger is neither (see below), so it is never a candidate.
2. **Rests in Parked, never Done.** A ledger's resting control state is `Pending`
   (Parked — the resting but non-finalised state), not `Running`. And the `Done`
   transition is excluded: a ledger never converges, so a stray `done` event
   collapses back to Parked rather than closing it. `restingState` folds both
   `Running` and `Done` to `Pending` for a `codebase` ticket.
3. **Terminates only by Archive.** The one terminal state a ledger reaches is
   `Archived` — the codebase is retired. Archive is an orthogonal board-membership
   axis (`DeriveArchived`), unchanged by the type, so a ledger archives like any
   ticket.

A ledger still surfaces to the human through the **normal state machine**: an open
escalation moves it to Needs-me exactly as it would any ticket. And it is
otherwise *just a ticket* — its log, `recall`, `log`, `supersedes`, and board
visibility are all unchanged, with no new flags. `recall <ledger>` and
`log <ledger>` operate on it as on any other ticket.

## Where it lives in the code

- **Schema / validation:** `internal/project` — `SpecMeta.Type`, the `TypeTicket`
  / `TypeCodebase` constants, and `NormalizeType` (absent → `ticket`;
  reject-unknown, so a typo like `codbase` fails loudly at author/load time rather
  than silently degrading a ledger to an ordinary ticket).
- **Control-state fold:** `internal/project` — `restingState` overlays the type
  onto the event-derived state inside `LoadAttempt`. Because a resting ledger is
  `Pending`, it is absent from the Running/attention rollups (which tally only
  Done/Review/Running/Needs-me) and invisible to the reconciler's admission test.
- **Authoring:** `cmd/new.go` — `draiver new X --type codebase --repo <path>`. An
  ordinary ticket writes no `type:` line, so it serialises byte-identically to a
  pre-drv-037 ticket.

The web-UI half — authoring and surfacing a ledger as a ledger on the board — is
drv-039; this ticket is the model/CLI half.
