# Draiver MVP Plan

> Agents are cattle, tickets are pets. The MVP makes a ticket a durable,
> portable, tamper-evident context store that a fresh agent can pick up from
> disk, and gives one human a read-only board of the tickets that need them.

## Scope

**In scope for MVP**

1. **CLI** — the shared human↔agent protocol (verbs over an append-only,
   hash-chained per-ticket log on the filesystem).
2. **Onboarding skill** — a Claude skill that teaches an agent to use the CLI:
   log gotchas and decision points, capture context *before* an escalation, and
   record what it did with the answer *after* one.
3. **Read-only web UI** — a self-contained HTMX server rendering the four
   control states as a live (auto-refreshing) board, with a per-ticket detail
   view that renders `spec.md` and the log timeline.

**Explicitly out of scope for MVP** (named in the README, deferred by request):

- SQLite cache/index — the filesystem log is the only backend.
- S3 sync / archival.
- Web UI **Manage mode** — spinning up agents, `stream-json` over stdio,
  WebSocket/SSE relay, worktree isolation, concurrent re-attempts. The MVP UI is
  read-only; it never writes and never launches processes.
- Issue-tracker join/sync.

## Decisions (locked)

| Area | Decision |
| --- | --- |
| Language | Go 1.26 |
| Module path | `draiver` (local module, no remote host implied) |
| CLI framework | **Cobra** — gives the README's "discoverable via `--help`" for free. *Default choice; say the word to swap for stdlib `flag`.* |
| Event vocabulary | Generic `draiver log --type <t> …`; `escalate`/`resolve`/`review`/`done` are thin semantic wrappers that also carry process-control meaning |
| Web UI | Live board (HTMX polling ~3s) + markdown-rendered ticket detail |
| Storage | Filesystem only, default `~/.draiver/data` (override `--data` / `DRAIVER_DATA`) |

Third-party libs (kept minimal): `spf13/cobra` (CLI), `gopkg.in/yaml.v3`
(frontmatter), `yuin/goldmark` (markdown→HTML in the UI). HTMX is vendored as a
single embedded JS file — no CDN, no build step.

## Storage layout

The ticket folder is the atomic, portable unit. Team/project/assignee are
fields, not folders. `spec.md` is shared at the ticket level; the mutable state
lives under one folder per **attempt** — a journey with its own working tree,
log, hash chain, and projection.

```
<data-root>/
  PROJ-123/
    spec.md                 # immutable input, SHARED across attempts
    attempts/
      0001/
        attempt.md          # provenance: tool, model, actor, started (+ reserved room)
        log/                # append-only, one write-once file per event; own hash chain
          20260803T160102Z-0001-created.md
          20260803T160415Z-0002-gotcha.md
          20260803T161230Z-0003-escalation.md
          20260803T164500Z-0004-resolution.md
        artefacts/          # blobs the log references, never inlines
        state.md            # generated projection — do not edit
      0002/                 # a separate attempt (different tool/model) — for comparison
        attempt.md  log/  artefacts/  state.md
```

- **Attempt id** is a zero-padded numeric directory (`0001`); tool/model live in
  `attempt.md`, not the name. Allocated race-safe (exclusive `mkdir` + retry).

- **Filename** `<ts>-<seq>-<type>.md`: basic-ISO-8601 UTC timestamp (no colons —
  filesystem-safe), zero-padded 4-digit sequence, and type. Sorts
  lexicographically into causal order; `seq` disambiguates same-second events.
- **Event type is a field, not a directory.** All structure lives in references
  between events (e.g. a resolution references its escalation), never in the
  tree.
- **Corrections are new events.** Nothing in `log/` is ever edited or deleted.

### Event file format

YAML frontmatter + markdown body:

```markdown
---
seq: 3
type: escalation          # created|note|gotcha|decision|escalation|resolution|review|done|assign
ts: 2026-08-03T16:12:30Z
actor: agent:claude-code   # or human:dave — free-form "kind:name"
ticket: PROJ-123
attempt: "0001"            # the attempt this event belongs to (in the hash — tamper-evident)
refs: []                   # e.g. a resolution sets refs: [3]
artefacts: []              # e.g. [artefacts/build-fail.log]
prev: 9f2c…                # hash of predecessor event ("" for genesis)
hash: 4a71…                # sha256 over this event's canonical form
---
Postgres 16 refuses the `vector` extension on the CI image. Tried building
pgvector from source — needs `postgresql-server-dev-16`, not in the image.
Need a decision on base image before I can proceed.
```

### Hash chain

`hash = sha256( prev || "\n" || canonical(frontmatter-sans-hash) || "\n" || body )`,
hex-encoded. `canonical()` marshals the frontmatter in a fixed key order with
the `hash` field omitted (a field can't hash itself). Each event's `prev` is the
predecessor's `hash`; the genesis event's `prev` is `""`.

Any edit to a body, a field, or the ordering breaks the chain — tamper-evidence
without relying on git. Attribution (`actor`, `ts`) lives in the event, not in
git blame, so the ticket stays portable.

### Sequence allocation & write-once

At append time the store scans the attempt's `log/` for the max `seq`, computes
the new event, and creates the file with `O_CREATE|O_EXCL` (fails if it exists).
On the rare `EEXIST` (a concurrent writer took the seq) it re-scans and retries.
Each attempt is its own chain: `seq` resets to 1 per attempt, and the genesis
`created` event has `prev: ""`.

## Control states (projection)

Derived per attempt from its log — the human's *relationship* to that attempt,
not progress. A ticket with several attempts yields several cards:

- `Running` — agent working; rendered as a **count**.
- `Needs me` — an unresolved escalation exists. **The board.**
- `Review` — agent claims done (a claim, not a fact).
- `Done` — closed.

Derivation (first match wins):

1. A `done` event is the latest lifecycle event → **Done**.
2. Any `escalation` whose `seq` is not referenced by a later `resolution` →
   **Needs me** (takes precedence over Review/Running — a block is the point).
3. The latest lifecycle event is `review` and no work/escalation follows it →
   **Review**.
4. Otherwise → **Running**.

`draiver status` writes this into each attempt's `state.md` (the generated
projection) and prints a board summary of attempts. `state.md` is never read back
as truth — it is regenerated from the log.

## CLI surface

Global: `--data <dir>` (or `DRAIVER_DATA`), `--actor <kind:name>` (or
`DRAIVER_ACTOR`; defaults from `$USER`), `--attempt <id>` (or `DRAIVER_ATTEMPT`;
defaults to the ticket's **latest** attempt).

| Verb | Purpose | Notes |
| --- | --- | --- |
| `new PROJ-123 --title "…" [--project --team --assignee --spec <file> --tool --model]` | Create ticket + `spec.md` (or import `--spec`) + attempt `0001` with its genesis event | |
| `attempt new PROJ-123 [--tool --model --from N]` | Start a new attempt (own log/chain/working tree); becomes the latest | The comparison unit |
| `attempt ls PROJ-123` | List attempts with tool, model, derived state, event count | |
| `log PROJ-123 --type gotcha "msg" [--ref N --artefact path]` | Append a typed event to the target attempt | The generic recorder |
| `escalate PROJ-123 "question" [--artefact path]` | Append an `escalation` **and halt with a nonzero exit code** | Gate enforced by process control |
| `resolve PROJ-123 N "answer"` | Append a `resolution` referencing escalation `seq N` in the target attempt | Human's answer |
| `review PROJ-123 ["claim"]` | Append a `review` event — agent claims done | Load-bearing claim |
| `done PROJ-123` | Append a `done` event | Terminal |
| `brief PROJ-123` | Replay `spec.md` + the target attempt's log into a context blob on stdout | If brief can't resume, the design is leaking state |
| `status [PROJ-123]` | Regenerate per-attempt `state.md`; print the attempt board summary | |
| `inbox [--mine]` | List unresolved escalations across all attempts of all tickets | `--mine` filters by assignee |
| `audit PROJ-123` | Verify the hash chain of every attempt (or one with `--attempt`) | nonzero exit + first broken link |
| `webui [--addr 127.0.0.1:7777] [--data <dir>]` | Run the read-only HTMX server | |

The write/read verbs act on `--attempt` / `DRAIVER_ATTEMPT` / the latest attempt.

**Exit codes**: `0` ok; `3` escalation raised (distinct, so a supervising loop
can branch on "blocked" vs "failed"); `1` usage/other error; `4` audit failure.
Documented in `--help` and honored by the skill.

## Onboarding skill

Ships in-repo at `skills/draiver-onboarding/SKILL.md` (installable to
`.claude/skills/`). It onboards a fresh agent handed a ticket **and an attempt**
id (it exports `DRAIVER_ATTEMPT` so every command targets its attempt). Contents:

1. **Cold-start**: run `draiver brief <TICKET>` first; treat its output as the
   full context. Never assume in-flight memory survives.
2. **Log gotchas** the moment you hit them:
   `draiver log <TICKET> --type gotcha "<what bit you and the workaround>"` —
   so the next agent doesn't rediscover them.
3. **Log decision points** with the alternatives and the choice:
   `draiver log <TICKET> --type decision "<chose X over Y because Z>"`.
4. **Before escalating**, capture the blocker as context (what you tried, what
   you need) with a `gotcha`/`note`, *then* `draiver escalate <TICKET> "<the
   crisp question>"`. Escalate halts the process (exit 3) — stop working; do not
   guess past a gate.
5. **After a resolution**, on resume you'll see it in `brief`; log how you acted
   on the answer (`--type note`) so the escalation→resolution→action arc is one
   durable trail.
6. **When you believe it's done**, `draiver review <TICKET>` (a claim for a human
   to verify) — not `done`, which the human owns.

The skill's throughline: *externalize everything valuable so your own context is
never precious.*

## Read-only web UI

Self-contained in the CLI binary (`draiver webui`). `html/template` +
`go:embed` for templates, static CSS, and vendored `htmx.min.js`. Renders from
the filesystem log on each request; **never writes, never spawns**.

Routes:

- `GET /` — the board shell. Four control states, one card **per attempt**
  (a ticket can appear several times). The `Needs me` column carries the most
  weight visually.
- `GET /board` — board partial, polled by `hx-get="/board" hx-trigger="every
  3s"` so the board stays live without a full reload.
- `GET /ticket/{id}` — attempt index: the ticket's attempts (tool/model/state),
  each linking to its detail.
- `GET /ticket/{id}/{attempt}` — detail: rendered `spec.md` + the attempt's
  provenance (tool/model), then its log as a timeline (type badge, `ts`, `actor`,
  body; resolutions visually linked to their escalation via `refs`).

Every meaningful element gets a stable `data-testid` (`board`, `col-needs-me`,
`col-review`, `count-running`, `count-done`, `attempt-link-<id>-<attempt>`,
`attempt-index`, `ticket-detail`, `spec`, `attempt-tool`, `event-<seq>`,
`state-badge`) so Playwright selectors are robust.

## Package layout

```
draiver/
  go.mod                       # module draiver
  main.go                      # thin: cmd.Execute()
  cmd/                         # cobra commands, one file per verb
    root.go new.go attempt.go log.go escalate.go resolve.go review.go done.go
    brief.go status.go inbox.go audit.go webui.go
  internal/
    store/     # data-root resolution, ticket + attempt paths, listing
    event/     # Event struct (incl. attempt), frontmatter (un)marshal, hashing
    ticketlog/ # per-attempt append (write-once/O_EXCL/seq), read+replay
    attempt/   # attempt Create (race-safe id, attempt.md, genesis), LoadMeta, Latest
    project/   # per-attempt control-state derivation, state.md generation
    brief/     # brief assembly (spec + one attempt's replayed log)
    audit/     # per-attempt chain verification, first-broken-link reporting
    web/       # server, handlers, embedded templates + static (go:embed)
  skills/
    draiver-onboarding/SKILL.md
  e2e/                         # Playwright (mirrors ../acp-portal setup)
    package.json playwright.config.ts global-setup.ts
    .fixture-data/             # seeded (by global-setup) board the UI serves under test
    tests/*.spec.ts
  docs/mvp.md
```

## Testing strategy

**Go unit tests** (CLI + web handlers), table-driven against `testdata/`
fixtures and `t.TempDir()`:

- `event`: frontmatter round-trip; hash determinism; `hash` excluded from its own
  input; `prev` linkage.
- `ticketlog`: append allocates monotonic `seq`; write-once refuses to clobber;
  replay yields causal order; **attempts have independent chains** (each starts at
  seq 1, and same body under different attempt ids hashes differently).
- `attempt`: `Create` allocates sequential ids with a genesis event; meta
  round-trips; `Latest` returns the highest id.
- `project`: control-state table; `LoadAll` returns one card per attempt with
  independent state.
- `brief`: output contains spec + every event of the attempt, escalation/resolution paired.
- `audit`: clean chain passes; a mutated body fails with the right first-broken
  `seq`; `VerifyTicket` covers every attempt; nonzero exit.
- `cmd`: `escalate` exits 3; `resolve` links by `seq`; `attempt new/ls`;
  `--attempt` targets a specific attempt and the default is the latest.
- `web`: `httptest` — board shows one card per attempt (a ticket appears twice),
  attempt index + detail render, and the server exposes **no** write routes.

**Playwright e2e** for the web UI, mirroring `../acp-portal` conventions
(`@playwright/test`, `webServer` boots the stack, `baseURL`, `data-testid`,
chromium project — browsers already cached):

- `webServer.command` builds & runs the binary against a fixture board seeded by
  `global-setup.ts` (which drives the real CLI, including a second attempt on one
  ticket); `reuseExistingServer` off in CI.
- Specs:
  - **smoke**: `/` loads, renders the four control states, header present.
  - **board**: per-attempt counts; the escalated attempt is in `Needs me`; the
    same ticket appears again as a second `Running` card.
  - **detail**: clicking `attempt-link-PROJ-101-0001` opens `/ticket/PROJ-101/0001`
    with the rendered spec, provenance, and log timeline; the attempt index lists
    both attempts.
  - **live refresh**: with polling active, the board reflects fixture state
    (kept deterministic; no runtime mutation needed for a green MVP).

## Implementation order

1. `event` + `ticketlog` + `store` (the durable core) with unit tests.
2. `new`, `log`, `escalate`, `resolve`, `review`, `done` verbs.
3. `project` + `status`/`state.md`, `brief`, `inbox`, `audit` + tests.
4. `webui` (board + detail + polling) + `httptest` handler tests.
5. Onboarding skill.
6. Playwright harness + specs.

## Open defaults (flag if you disagree)

- **Cobra** as the CLI framework (vs stdlib `flag`).
- Escalation exit code **3**; audit-failure **4**.
- Default data root `~/.draiver/data`; web UI binds `127.0.0.1:7777`.
- `actor` defaults from `$USER` as `human:$USER` unless `--actor`/`DRAIVER_ACTOR`
  set (agents pass `agent:<name>`).
