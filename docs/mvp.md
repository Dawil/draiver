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
fields, not folders.

```
<data-root>/
  PROJ-123/
    spec.md         # immutable input: frontmatter identity + design body
    log/            # append-only, one write-once file per event
      20260803T160102Z-0001-created.md
      20260803T160415Z-0002-gotcha.md
      20260803T161230Z-0003-escalation.md
      20260803T164500Z-0004-resolution.md
    artefacts/      # blobs the log references, never inlines
    state.md        # generated projection — do not edit
```

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

At append time the store scans `log/` for the max `seq`, computes the new event,
and creates the file with `O_CREATE|O_EXCL` (fails if it exists). On the rare
`EEXIST` (a concurrent writer took the seq) it re-scans and retries. This keeps
writes strictly additive and safe under the single-agent-per-ticket assumption
of the MVP.

## Control states (projection)

Derived from the log — the human's *relationship* to the ticket, not progress:

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

`draiver status` writes this into each ticket's `state.md` (the generated
projection) and prints a board summary. `state.md` is never read back as truth —
it is regenerated from the log.

## CLI surface

Global: `--data <dir>` (or `DRAIVER_DATA`), `--actor <kind:name>` (or
`DRAIVER_ACTOR`; defaults from `$USER`).

| Verb | Purpose | Notes |
| --- | --- | --- |
| `new PROJ-123 --title "…" [--project --team --assignee --spec <file>]` | Create ticket folder, `spec.md` skeleton (or import `--spec`), and the genesis `created` event | |
| `log PROJ-123 --type gotcha "msg" [--ref N] [--artefact path]` | Append a typed event (gotcha, decision, note, …) | The generic recorder |
| `escalate PROJ-123 "question" [--artefact path]` | Append an `escalation` event **and halt with a nonzero exit code** | Gate enforced by process control, not agent goodwill |
| `resolve PROJ-123 N "answer"` | Append a `resolution` referencing escalation `seq N`; escalation+resolution become one durable artefact | Human's answer |
| `review PROJ-123 ["claim"]` | Append a `review` event — agent claims done | Load-bearing claim |
| `done PROJ-123` | Append a `done` event | Terminal |
| `brief PROJ-123` | Replay `spec.md` + log into a single context blob on stdout that cold-starts a fresh agent | If brief can't resume the work, the design is leaking state |
| `status [PROJ-123]` | Regenerate `state.md` projection(s); print board summary | |
| `inbox [--mine]` | List unresolved escalations across all tickets; `--mine` filters by assignee | |
| `audit PROJ-123` | Recompute and verify the hash chain; nonzero exit + first broken link on failure | |
| `webui [--addr 127.0.0.1:7777] [--data <dir>]` | Run the read-only HTMX server | |

**Exit codes**: `0` ok; `3` escalation raised (distinct, so a supervising loop
can branch on "blocked" vs "failed"); `1` usage/other error; `4` audit failure.
Documented in `--help` and honored by the skill.

## Onboarding skill

Ships in-repo at `skills/draiver-onboarding/SKILL.md` (installable to
`.claude/skills/`). It onboards a fresh agent that has just been handed a ticket
ID. Contents:

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

- `GET /` — the board shell. Four control states: `Running` and `Done` as
  counts, `Needs me` and `Review` as lists of ticket cards. The `Needs me`
  column carries the most weight visually.
- `GET /board` — board partial, polled by `hx-get="/board" hx-trigger="every
  3s"` so the board stays live without a full reload.
- `GET /ticket/{id}` — detail: rendered `spec.md`, then the log as a timeline
  (type badge, `ts`, `actor`, body; resolutions visually linked to their
  escalation via `refs`).

Every meaningful element gets a stable `data-testid` (`board`, `col-needs-me`,
`col-review`, `count-running`, `count-done`, `ticket-link-<id>`,
`ticket-detail`, `spec`, `event-<seq>`, `state-badge`) so Playwright selectors
are robust.

## Package layout

```
draiver/
  go.mod                       # module draiver
  main.go                      # thin: cmd.Execute()
  cmd/                         # cobra commands, one file per verb
    root.go new.go log.go escalate.go resolve.go review.go done.go
    brief.go status.go inbox.go audit.go webui.go
  internal/
    store/     # data-root resolution, ticket & log paths, listing
    event/     # Event struct, frontmatter (un)marshal, canonicalization, hashing
    ticketlog/ # append (write-once/O_EXCL/seq), read+replay in order
    project/   # control-state derivation, state.md generation
    brief/     # brief assembly (spec + replayed log)
    audit/     # chain verification, first-broken-link reporting
    web/       # server, handlers, embedded templates + static (go:embed)
  skills/
    draiver-onboarding/SKILL.md
  e2e/                         # Playwright (mirrors ../acp-portal setup)
    package.json playwright.config.ts
    fixtures/board/            # seeded tickets the UI serves under test
    *.spec.ts
  testdata/                    # Go fixture tickets (golden logs)
  docs/mvp.md
```

## Testing strategy

**Go unit tests** (CLI + web handlers), table-driven against `testdata/`
fixtures and `t.TempDir()`:

- `event`: frontmatter round-trip; hash determinism; `hash` excluded from its own
  input; `prev` linkage.
- `ticketlog`: append allocates monotonic `seq`; write-once refuses to clobber;
  replay yields causal order.
- `project`: control-state table — running, unresolved-escalation→Needs me,
  resolved→Running, review→Review, done→Done, and the precedence rules.
- `brief`: output contains spec + every event, with escalation/resolution paired.
- `audit`: clean chain passes; a mutated body, a swapped field, and a reordered
  file each fail with the correct first-broken `seq`; nonzero exit.
- `cmd`: `escalate` exits 3 and appends exactly one escalation; `resolve` links
  by `seq`; `new` scaffolds correctly. Driven via Cobra command execution.
- `web`: `httptest` against a fixture data dir — board shows correct
  counts/lists, detail renders spec + timeline, and the server exposes **no**
  write routes (read-only contract).

**Playwright e2e** for the web UI, mirroring `../acp-portal` conventions
(`@playwright/test`, `webServer` boots the stack, `baseURL`, `data-testid`,
chromium project — browsers already cached):

- `webServer.command` builds & runs the binary against a seeded fixture board:
  `go run . webui --data e2e/fixtures/board --addr 127.0.0.1:7788`, `url`
  pointed at it, `reuseExistingServer` off in CI.
- Specs:
  - **smoke**: `/` loads, renders the four control states, header present.
  - **board**: `Needs me` lists the escalated fixture ticket(s); `Running`/`Done`
    show correct counts.
  - **detail**: clicking `ticket-link-PROJ-123` opens the detail view with the
    rendered spec and a log timeline; an escalation event shows its linked
    resolution.
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
