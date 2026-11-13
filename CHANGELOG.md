# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Dependency edges in `spec.md` — `wants:`/`after:`/`requires:` (drvctl-037).**
  The unit file gains its first *relational* fields: three optional frontmatter
  lists of ticket ids — `wants:` (enable-propagation), `after:` (gate on a
  predecessor reaching Review), `requires:` (gate on Done); semantics per
  `docs/capabilities-and-supervision.md`. This is the **data model only** — no
  reconciler consumes the edges yet, and `status`/reconciler output is unchanged.
  A new authoring verb **`draiver depends TICKET --wants … --after … --requires
  …`** merges edges into the spec's frontmatter the same way `draiver title`
  edits `title:` — metadata *outside* the hash-chained log, so it never touches
  `draiver audit`. Each flag is repeatable/comma-separated and **adds** to the
  existing set rather than replacing it; the injected line is canonical inline
  YAML (`wants: [A, B]`), replacing any prior inline-or-block entry while leaving
  the rest of the spec — body, comments, untouched keys — intact. The model layer
  exposes `project.ReverseWants` (who wants X), the substrate for reverse-wants
  activation (drvctl-041). The three relations form a DAG: at author time a
  **self-edge or any edge that would close a cycle is refused** with the offending
  path (`A → B → A`), across the union of all three relation kinds, and a refused
  edit rewrites nothing.
- **Edit dependency edges from the web UI (drvweb-016).** The attempts-index page
  (`/ticket/{id}`) gains a quiet **Dependencies** editor — the drvweb-009
  provenance inline-edit style reused, but scoped to `spec.md` (ticket-level,
  shared across attempts) rather than per-attempt `attempt.md`, since edges are
  spec-level design input. Each relation (`wants`/`after`/`requires`) prefills as
  a comma-separated id list you edit in place and save on blur — no Save button —
  with an `(i)` tooltip explaining its reconciler semantics. The write shells the
  drvctl-037 authoring verb (the web layer never writes `spec.md` itself, its
  first web writer of spec frontmatter); a **cycle or self-edge refusal renders
  inline** and writes nothing to disk. Because the field is a whole-set WYSIWYG
  editor, the verb gains a **`--set` flag**: each relation you pass *replaces* its
  set instead of adding to it (a blank value clears that relation; omitted
  relations are untouched), so deleting an id in the UI actually removes the edge.
- **Transitive enable/disable down `wants:` — desired-ness flows to children
  (drvctl-038).** The reconciler's `desired()` gains a third path,
  **enabled-via-parent**: an attempt is desired if it is directly enabled *or* a
  ticket that (transitively) `wants:` it has a desired attempt. Enabling a
  Capability with `wants: [A, B]` now pulls A and B into the supervised fleet
  alongside it — and grand-children through an intermediate the parent only
  *wants* (not one that is itself directly enabled). For each wanted child the
  loop targets its **latest attempt** when that is `Running`; a child with **no
  attempt yet** gets one **minted with the default launch config** (the
  `ctl up --repo` fallback, default adapter, base-spec model — the same start a
  manual `enable` gives it). Propagation is a **derived** computation re-run every
  tick, not a stamped bit, so **disable withdraws symmetrically for free**: a
  disabled parent simply stops sourcing its children next tick, while a child that
  is *also* directly enabled stays desired. A child whose latest attempt is a
  Review claim, a Needs-me block, or a closed Done is **left alone** — parent
  desired-ness never force-admits a non-Running attempt nor re-mints over a
  terminal one (auto-freshness on a spent child is the deferred
  *new-attempt-on-retrigger* follow-on). Cycles remain refused at authoring
  (drvctl-037); a visited-guard keeps a hand-edited cyclic spec from spinning
  regardless. `enable`/`disable` stay a pure per-attempt log bit — no fan-out is
  written to disk.
- **The `Pending` control state — a runtime projection (drvctl-039).** A fifth
  control state below Needs-me on the attention scale: an attempt that is
  **enabled, has no live agent, and no open escalation** — a *self-resolving wait*
  that advances on a gate opening or an activation firing, needing **zero human
  attention** (Needs-me needs a human; Pending needs nothing). It renders as a
  quiet count, like Running. Pending is a **read-time projection, never a log
  event** — the hash chain stays clean (tier discipline). Crucially it is kept
  **off `Attempt.State`**: the reconciler admits precisely the log-`Running`
  attempts, so folding Pending into the stored state would hide desired-but-not-
  yet-live attempts from admission. Instead `project.Control(state, enabled, live)`
  (and the `Attempt.Control()` method over a new runtime `Live` bit) overlays
  Pending onto Running only when enabled and no live agent; every other state
  passes through untouched. Liveness is a `session.json` pid + signal-0 probe,
  exposed as `session.Alive` — the CLI/project tier reads the OS itself rather than
  asking the daemon, mirroring `reconcile.OSProc.Alive`. `draiver status` now
  prints a **`Pending`** count at the quiet end of the summary line. `state.md`
  regeneration is unchanged — it stays the pure log projection (`Running`). The
  human-facing *reason* ("waiting on X") and board rendering land with drvweb-015 /
  drvctl-040 once forward gates exist.
- **Forward success gate — `after:`/`requires:` hold admission until a
  predecessor succeeds (drvctl-040).** The first real scheduler increment: the
  reconciler's `desired()` gains a final **gate** pass that holds a desired
  attempt *Pending* (out of admission) until its ordering predecessors succeed.
  `after: [X]` admits once **X reaches Review** (a success claim); `requires: [X]`
  admits once **X reaches Done** (the merged terminal). A predecessor's threshold
  is the **best state across all its attempts** — success is monotonic, so a later
  re-run of a predecessor never retracts a threshold an earlier attempt crossed —
  and *every* named predecessor must clear for a multi-edge gate to open. The gate
  is **edge-triggered and fire-once**: it holds only a *never-admitted* dependent
  out; once a dependent is admitted (a session id is on record), a later change in
  a predecessor — a Review flickering back to Running — never re-closes the gate,
  so an already-running dependent is never reaped or restarted (staleness surfaces
  at the integration test, not here). The latch is the durable **session.json**,
  so it survives a `ctld` restart — an already-admitted dependent resumes rather
  than re-gating. The gate runs *after* `wants:` propagation, so enable still flows
  down from a gate-held (Pending) parent; the gate trims *admission*, not
  desired-ness. No new log event and no on-disk scheduler state — the gate is a
  derived computation re-run every tick.
- **Per-attempt prompt-cache telemetry — cache tokens persisted, derived and
  priced (drvctl-031).** The adapter already read `cache_read` /
  `cache_creation` per request (drvctl-012), but nothing surfaced them per
  attempt. `session.Meter` (`meter.json`) now carries a cumulative `Totals`
  alongside the live-gauge `Usage` snapshot: `watch` sums input, output,
  cache-read and cache-creation tokens from the **per-request frames only** (the
  cumulative turn-end frame is skipped, so a turn is never double-counted), and
  the totals accumulate across turns and respawns. `agent.Totals` derives
  `cache_hit_ratio` (`cache_read / (cache_read + cache_creation + input)`),
  `normalized_work` (all tokens at 1x, for A/B comparisons), `billed_input_tokens`
  and `caching_active` (true iff any cache field is non-zero — catches
  below-min-prefix and silent-invalidator misses). The derived fields are written
  to the file but always recomputed from the raw counts on read, so they cannot
  desync. On every retire (terminal or park) the reconciler folds the final tally
  into `attempt.md` as a `metrics` block — the reserved metrics room made real.
  Pricing lands as exported multipliers (cache read 0.1x, write 1.25x for 5m TTL /
  2x for 1h TTL); dollar cost still comes from the adapter's `total_cost_usd`,
  which is already cached-rate priced, so no existing quota math changed. This is
  the measurement foundation for the rest of the prompt-caching set
  (`docs/prompt-caching.md`).
- **Prompt-cache panel on the attempt detail page (drvweb-012).** The attempt
  view (`/ticket/{id}/{attempt}`) gains a cache panel above the spec that shows the
  drvctl-031 telemetry, so checking whether caching worked no longer means reading
  `meter.json` by hand. `caching_active` is the headline yes/no, followed by cache
  read vs written tokens, hit ratio, normalised work, and billed vs uncached
  input-rate token equivalents with the fraction saved. These are token
  equivalents, not dollars, because the metrics carry no price. A
  `caching_active: false` attempt is visually distinct: the panel turns amber
  (`.cache-inactive`) and the flag shows ✕ "the prefix never cached", so the
  signal does not rely on colour alone. The metrics are only folded in when an
  attempt retires with a metered session, so an in-flight attempt shows a quiet
  "recorded when this attempt retires" placeholder instead of a grid of zeros.
  `project.Attempt` now exposes the folded `agent.Metrics`. The cross-attempt
  amortisation view is left to drvweb-013.
- **Prompt-cache config surface — `exclude_dynamic_system_prompt_sections` and
  `append_system_prompt_file` (drvctl-032).** Two new daemon config keys, both
  default off/empty, unlock cross-worktree/cross-ticket prompt-prefix sharing
  (`docs/prompt-caching.md`, the M0 unlock). `exclude_dynamic_system_prompt_sections`
  (bool) makes the Claude Code adapter pass `--exclude-dynamic-system-prompt-sections`,
  moving the per-machine system-prompt sections (cwd, env, memory paths, git status)
  into the first user message so every worktree shares a byte-identical prefix;
  `append_system_prompt_file` (path) supplies draiver's protocol text as
  `--append-system-prompt`. The file is read **once** when `ctld` builds its
  reconciler (`resolvePromptCache` in `cmd/ctl.go`), so the appended text is
  byte-uniform across every attempt; an unreadable file is a hard error rather than
  a silently empty prefix. With the exclude toggle on, start-up probes the pinned
  `claude` (`claudecode.SupportsExcludeDynamicSystemPrompt`) and **fails loud** if
  it does not accept the flag (pairs with the drvctl-033 version pin). With both
  keys unset the launch is byte-identical to before. Attempt-level A/B cohorts are
  left to drvctl-036/drvweb-013.
- **`draiver canary` — a silent-invalidator canary for the shared prompt-cache
  prefix, plus the flag-on/flag-off validation run (drvctl-036).** The capstone of
  the prompt-cache change set (drvctl-032/033/034). A new read-only verb, **`draiver
  canary`** (backed by `internal/canary`), scans each repo's attempts and checks
  that the second and later attempts reuse the shared tools+system+append prefix on
  **turn 1** — keyed on the first usage frame in `stream.jsonl`, since within-session
  caching swamps the session-aggregate meter and cannot tell a healthy prefix from a
  busted one. When reuse attempts start cold-writing the prefix (per-ticket data
  slipped into `--append-system-prompt`, an unpinned adapter upgrade) the repo is
  marked `FIRE` and the verb **exits 5**. Per attempt it prints a table — turn-1
  read/create, **`t1-rd:cr`** (turn-1 `cache_read/cache_creation`, the unbounded
  ">100%" amortisation metric), read share, hit ratio, normalised work, billed
  input and a verdict (`ok`/`COLD`/`CACHING-OFF`) — so it doubles as the per-repo
  cross-attempt cache rollup. Flags: `--repo` scopes to one repo, `--min-prefix`
  (default 1024 tokens) and `--read-share-floor` (default 0.5) tune the cold
  threshold. Adapter-version attribution is a seam that reports "version
  unavailable" until drvctl-033 records version events. The new
  **`docs/cache-validation.md`** records the protocol and verdict: over six on-arm
  attempts with `exclude_dynamic_system_prompt_sections` on, turn-1 cache reads
  grew (16.8K→17.6K), turn-1 cold-write fell 17%, turn-1 read:creation rose from
  228% to 335%, and cwd/git/path behaviour showed 6/6 parity — so the flag was made
  the operator default in `~/.draiver/config.json` (the code default in
  `internal/config` is unchanged).
- **Cross-attempt, per-repo prompt-cache rollup with A/B cohorts (drvweb-013).**
  The caching win is cross-ticket amortisation — every attempt after the first on
  a repo reads the shared prefix at ~10% — which the per-attempt cache panel
  (drvweb-012) cannot show. A new **`/cache`** page, linked from the board topbar
  ("Cache rollup →"), aggregates every metered attempt per repo: hit ratio, cache
  read/written, normalised work, billed-vs-uncached input-equivalents, and saved
  fraction. Aggregation (`repoRollups` in `internal/web/rollup.go`) is pure and
  deterministic and sums raw token counts before deriving ratios — never averaging
  per-attempt ratios. **A/B cohorts** split attempts by whether caching actually
  engaged (`agent.Metrics.CachingActive`) and compare the on/off cohorts side by
  side on normalised cost and outcome (terminal-state distribution). A per-repo
  **caching-health line** reads `ok` when reads dominate, `warn` when cache
  creation dominates (the silent-invalidator smell of a busted shared prefix), and
  `off` when caching never engaged. Because no per-attempt cache-config tag exists
  yet (drvctl-032 deferred it), a flag-on run whose prefix busted lands in the off
  cohort; the page caption says so. The "saved" projection (`cacheCost`) is now
  shared with the per-attempt panel so the figure means the same on both. The page
  is a static report, not a polled region.
- **Cache-win metrics on the attempt prompt-cache panel — cost avoided, billed vs
  uncached, and the reuse factor (drvweb-018).** The drvweb-012 panel led with
  `cache_hit_ratio`, a whole-session average bounded at 1 that understates reuse and
  carries no cost figure. `agent.Metrics` gains three derivations from the existing
  `Totals` (no new capture): **`ReadCreationRatio`** (`cache_read/cache_creation`,
  the unbounded ">100%" reuse factor; a `*float64` that is `null`/"—" when nothing
  was written), **`CostAvoidedInputTokens`** (uncached baseline minus
  `BilledInputTokens()`, i.e. `0.9·read − 0.25·creation`, deliberately unclamped so
  a write-never-read session shows negative) and **`PctCostAvoided`** (that as a
  share of baseline). The panel now leads with **equivalent API cost avoided** ($
  and % of prompt spend — a modelled figure, since the daemon runs on subscription
  OAuth), a **billed-vs-uncached** dollar bar, and the **reuse factor** (`×N` /
  `N%`); the old ratio is relabelled "share served from cache", and every metric
  carries a provenance-style `(i)` hover explaining it and what a good result looks
  like. A minimal input-price lookup in `internal/web/pricing.go` prices
  opus-4.8 at $5/1M; an unknown model falls back to input-equivalent tokens with no
  `$`. `attempt.parseMeta` recomputes the derived fields from the raw token counts,
  so attempts metered before this change no longer read as `0`. Because attempts
  don't yet persist a resolved model id, live panels currently show the
  token-equivalent figures rather than dollars.
- **Pin the Claude Code adapter version for a session's life and gate auto-update
  (drvctl-033).** A background Claude Code upgrade shifts the preset system prompt
  and tool definitions, busting the shared prompt-cache prefix fleet-wide and
  silently eroding the drvctl-032/034 caching win on every release. The control
  plane now holds that invariant. **Record:** a new `claudecode.Version` probes
  `claude --version` (`2.1.216 (Claude Code)` → `2.1.216`) once at daemon start;
  the reconciler pins it into every session's provenance as `AdapterVersion` in
  `session.json`, folded into `attempt.md` as `adapter_version:` on retire beside
  `protocol_version`. **Pin:** supervised sessions now launch with
  `DISABLE_AUTOUPDATER=1` alongside `ENABLE_PROMPT_CACHING_1H=1`, so no
  mid-session upgrade can occur — upgrades are operator-driven, between sessions.
  **Attribute:** the drvctl-036 canary's `adapterVersion` loader (previously a
  stub returning `""`) now reads the recorded version, so a busted turn-1 prefix
  is attributed to a version bump rather than reported as "version unavailable".
  A failed version probe is non-fatal — `draiverctld` logs it, records an empty
  version, and the canary degrades to "unattributable" rather than refusing to
  boot.
- **Archive an attempt from its board card — green tick to accept, grey cross
  to close (drvweb-019).** Until now nothing could take a card off the board, so it
  only grew. Each card now has a hover-revealed control: a **green ✓** on Done
  cards (accept and file away) and a **grey ✕** on Running / Stuck / Review cards
  (close an attempt that will not finish). Both ask for an `hx-confirm`, then
  `POST /ticket/{id}/{attempt}/archive`, and the board region re-renders so the
  card disappears and the column counts update in one swap. Archive is a
  **reversible flag derived from the log**, mirroring `enable`/`disable`: new
  `archive` / `unarchive` event types and `project.DeriveArchived` (last event
  wins, default off), exposed as `project.Attempt.Archived`. It is about board
  membership only and is independent of the attempt's state; the existing `done`
  event is not reused. The sentiment travels in a new `outcome` frontmatter field
  (`accepted` | `abandoned`), which the server works out from the attempt's
  state. The field is omitempty, so hashes of existing events are unchanged. The
  route writes through `ticketlog.Append` directly (there is no CLI verb), is
  idempotent and same-origin guarded, and `board()` skips archived attempts. The
  cross records board membership only and does not stop a session. There is no
  un-archive button yet; an `unarchive` event restores a card.
- **Reverse-`wants:` activation — a child's escalation wakes its Pending
  coordinator (drvctl-041).** The systemd `OnFailure=` analog: enable flows
  *down* a `wants:` edge (drvctl-038), and now escalation flows *up* the same
  edge. A new additive contributor, `activateReverseWants`
  (`internal/reconcile/reverse_wants.go`), runs last in the reconciler's
  `desired()`: when a ticket X has an open escalation, every enabled ticket W whose
  `spec.md` `wants:` X (found via drvctl-037's `project.ReverseWants`) is made
  desired, so a dormant coordinator is woken to assess. Running after the forward
  gate means the wake overrides the coordinator's own shut `after:`/`requires:`
  gate — a child crying for help is exactly when it should run. It is narrower than
  forward propagation: it never mints an attempt (nothing to wake if the
  coordinator has none) and it respects a human `disable`. Children never name
  their coordinator. Union-only, so it cannot regress the direct/wants/gate passes.
  The spec's StartLimit loop guard is **not** implemented — the StartLimit ceiling
  does not exist yet, so that acceptance test is skipped.
- **`draiver archive` / `draiver unarchive` — first-class CLI verbs for board
  archive, with sentiment (drvctl-043).** Board archive (drvweb-019) was the one
  webui write with no CLI verb, and the only terminal route — `draiver log … --type
  archive` — could not set the `Outcome` field, so a CLI archive was
  sentiment-blind. The new **`draiver archive <ticket[@attempt]>`** appends an
  `archive` event with **`--accepted`** (the green-tick sense) or **`--abandoned`**
  (the grey-cross sense); with neither flag it defaults to the State-derived
  sentiment exactly as the board does (Done → `accepted`, else `abandoned`), and
  passing both is rejected before any write. **`draiver unarchive
  <ticket[@attempt]>`** appends an `unarchive` event (no outcome; last-wins via
  `DeriveArchived`), returning the attempt to its lifecycle column. Outcome strings
  and the `archiveBody` log prose match `internal/web` verbatim, so board- and
  CLI-authored archives are indistinguishable in the log and to metrics. Both verbs
  are idempotent — re-archiving an archived attempt or unarchiving a live one
  reports the current state and exits 0 without a second event — and a missing
  ticket/attempt errors and writes nothing. Target parsing reuses
  `resolveCtlTarget`; `cmd/root.go`'s `appendEvent` is split into a resolver plus a
  new `appendEventAt` core the verbs share. The webui repoint is drvweb-020.
- **The board renders the Pending control state with a "waiting on X" reason
  (drvweb-015).** The zero-attention Pending tier from drvctl-039 now appears on
  the web board (`GET /`, `/board`). It is a quiet count nested *under* Running in
  the same vertical rather than a fifth column, with one card per Pending attempt.
  `board()` now fills the runtime `Desired`/`Live` bits and switches on
  `a.Control()`, the same overlay `draiver status` applies. A desired, log-Running
  attempt with no live agent therefore lands in the Pending bucket. Liveness is
  checked through the injectable `s.alive`, so it agrees with the session dot.
  Each card shows a quiet reason line from the new
  `project.WaitingReason(ticket, all, edges)` (stored on a runtime
  `Attempt.WaitingReason` field). The function works like a read-side copy of
  the forward gate: it names the predecessors still holding the gate shut
  (`after:` short of Review, `requires:` short of Done) as *"waiting on SRC-1"*.
  If no edge is holding the attempt and it is only waiting for admission, it
  reads *"waiting to start"*. Stable testids are `col-pending`, `count-pending`
  and `waiting-<ticket>-<attempt>`. The usual 3s board poll recomputes
  `Control()` on every render, so a card moves from Pending to Running when its
  gate opens and an agent is admitted. The e2e seed gains a Pending fixture with a
  Playwright assertion. This change also repairs `e2e/global-setup.ts`, which had
  been broken since `--repo` became required at creation.
- **Coordinator supervision dial — `passthrough` and `pre-digest` (drvctl-042).**
  A per-ticket supervision mode now gates drvctl-041's reverse-`wants:` activation:
  on a sub-ticket escalation, the mode of the Capability that `wants:` it decides
  whether its coordinator wakes. **`passthrough`** (the floor and built-in default)
  routes the escalation straight to Needs-me and spawns no coordinator;
  **`pre-digest`** wakes the coordinator to assess across its children and post one
  consolidated escalate-and-recommend for a human to ratify. A new verb **`draiver
  ctl supervision <ticket[@attempt]> <passthrough|pre-digest>`** records the mode as
  a `supervision` log event (mode on `Outcome`, last event wins, replayed by
  `brief`) — a mutable control setting like `enable`/`disable`. The global/project
  default comes from the new config key **`default_supervision`**; a bad value fails
  daemon start. Auto-execute is deferred and refused by name. The model lives in
  `internal/project/supervision.go` (`ParseSupervision`, `DeriveSupervision`,
  `Effective`); the reconciler's `wakeWanter` only wakes a coordinator whose
  effective mode is `pre-digest`. A coordinator never writes into a child's log.
- **Capability view — a ticket's `wants:` sub-fleet and its supervision dial on the
  detail page (drvweb-017).** When a ticket carries `wants:` edges (a Capability),
  its attempt detail page (`ticket.html`) gains a **Capability** panel listing each
  wanted child with its latest attempt's control state — Pending folded in via
  `DeriveDesired` plus the live-session probe, exactly as the board does — and, for
  a Pending child, the `WaitingReason` gate peek ("waiting on X"); a wanted ticket
  with no attempt yet shows "no attempt yet". Below it, a **Supervision** radio
  dial (passthrough / pre-digest, with an `(i)` explainer) posts on change — no
  Save button — to the new `POST /ticket/{id}/{attempt}/supervision` route, which
  shells `draiver ctl supervision` (drvctl-042) so the web layer keeps one write
  path, then swaps the panel in place with a "saved" confirmation. The dial
  pre-selects the **effective** mode (per-attempt override folded over the config
  default) and notes "inheriting default (…)" when no override is set; the default
  is threaded in from `cmd/webui.go` through a new `WithSupervisionDefault` server
  option. Panel and POST are both gated to Capabilities, so an ordinary ticket
  neither shows nor accepts the dial. Graph editing remains drvweb-016's
  Dependencies editor.
- **`internal/pyramid` — load and validate `.test-pyramid.yaml` (drvctl-047).**
  First rung of the test-pyramid epic (drv-009), data model only — nothing executes
  yet. `pyramid.Load(worktreePath)` reads a repo's `.test-pyramid.yaml`: an ordered,
  bottom-to-top `levels:` list of `{name, run}` entries, where the list order *is*
  the pyramid and `Target()` returns the top (last) rung. A **missing file is a
  valid "no pyramid" state** (`nil, nil`, like `config.Load`); a present but
  malformed or invalid file is an error, so typos surface. Validation requires a
  non-empty `levels`, unique non-blank names and a non-blank `run`. Unknown keys
  are ignored, so later `up`/`down`/`healthcheck`/`timeout` fields can be added
  without breaking older binaries.
- **`draiver test [RUNG] [--log]` — deterministic test-pyramid executor
  (drvctl-048).** Run from inside an attempt worktree, `draiver test` loads the
  repo's `.test-pyramid.yaml` (via drvctl-047's `internal/pyramid`) and climbs from
  the base rung up to `RUNG` (default: the top), streaming each rung's command and
  stopping at the first non-green one; a repo with no pyramid reports "nothing to
  test". **Bare**, it only runs and reports — the fast iterative loop, fine on a
  dirty tree. **With `--log`** it refuses a dirty tree up front and, only when every
  rung through `RUNG` is green, appends one hash-chained **`test-result`** event
  carrying the rung and HEAD commit, so `brief` replays "rung green @ sha" for the
  next agent; a failing run logs nothing. The attempt is identified from the
  checkout itself (new `internal/worktree/identify.go` — `Identify`, `HeadSHA`,
  `DirtyAt`) rather than `$DRAIVER_ATTEMPT`. The new event's `Rung`/`Commit` fields
  are `omitempty`, so every other event still hashes byte-identically. The handbook
  and onboarding `SKILL.md` gain the iterate → commit → `test --log` → `review` step.
- **`draiver review` refuses a claim below the test-pyramid target rung at HEAD
  (drvctl-049).** Closes the loop for the test-pyramid epic (drv-009): the review
  claim now enforces, by process control, that the target (top) rung was actually
  climbed — the same seam as the permission gate and `merge`'s clean-checkout gate.
  Before appending, `cmd/review.go` resolves the *attempt's* worktree (from
  `attempt.md`'s repo via a new read-only `worktree.Manager.Locate`, not the cwd, so
  review can still be run from outside a worktree), loads its
  `.test-pyramid.yaml`, and folds the attempt's `test-result` events with the new
  `pyramid.HighestAtHEAD`: the highest logged rung whose `commit` equals the
  current HEAD. If that is below the target, the claim is refused with a message
  naming the gap and the fix — climb it with `draiver test --log`, then claim
  review. A result logged before a later commit is **stale** and does not count, so
  committing after logging correctly invalidates the claim. The gate is additive
  and fails open: no recorded repo, a non-git base, no live checkout, or no
  `.test-pyramid.yaml` all skip it and review proceeds unchanged; only a genuine git
  fault on a real base surfaces as an error.
- **draiver dogfoods its own test pyramid with a Testcontainers integration rung
  (drv-010).** The repo now has a checked-in `.test-pyramid.yaml` with a `unit`
  rung (`go test ./...`) and an `integration` rung
  (`go test -tags=integration ./internal/e2e/integration/...`). drv-009's Review
  gate therefore runs against draiver's own tickets. The integration tests sit
  behind the `integration` build tag, so the unit rung stays fast and needs no
  containers. When it shipped, integration was the target (top) rung; drv-025
  later made it advisory. A new harness package, `internal/e2e/integration`, lets
  each test declare the real dependencies it needs:
  - a Gitea forge container (`forge.go`)
  - reproducible data-root fixture volumes `empty-board`, `mid-escalation` and
    `review-ready-to-merge` (`dataroot.go`)
  - seeded git repos-under-test (`gitrepo.go`)
  - the served webui binary (`webui.go`)

  The runner builds once and skips cleanly when no container runtime is present
  (`runtime.go`). Teardown is deterministic through `t.Cleanup` plus the
  Testcontainers Reaper. The first suite covers a forge round-trip, checking
  `ctl merge --remote` containment against a live forge: a contained branch goes
  to `done`, while an uncontained one is refused, records nothing and honours
  `--escalate`/`--no-escalate`. It also runs webui scenarios against seeded
  volumes: board columns, one card per attempt and tallies, the review column, an
  empty board, and Markdown sanitization (an XSS guard). `docs/integration-testing.md`
  explains how to run the rung locally and notes that it needs Docker.
- **Test-pyramid badge on the board card and attempt header — the cleared rung at
  HEAD (drvweb-021).** The UI half of the test-pyramid epic (drv-009): a logged
  `test-result` was otherwise invisible on the board. Board cards (beside the
  session dot) and the attempt-detail `<h1>` now render a compact pill such as
  `unit ✓ · integration ✓` — every rung up to the highest one logged at the
  attempt's current HEAD, folded with drvctl-049's `pyramid.HighestAtHEAD`.
  Distinct states: **green** (result at HEAD, clean tree); **stale** (results
  exist but at an older commit — dimmed, dashed, showing the recorded short SHA,
  `data-stale`); **dirty** (a result names HEAD but the worktree has uncommitted
  changes — dimmed, flagged `uncommitted`, `data-dirty`, so green means re-running
  `draiver test` would still pass); and **none** (no `.test-pyramid.yaml` or no
  result — nothing rendered). It is a read-only projection with no write path and
  no stored SHA: HEAD and cleanliness are read live from the attempt's worktree
  (`worktree.HeadSHA`, `worktree.DirtyAt`), falling back — when the daemon has
  reclaimed the checkout on retire into Review — to the branch tip and committed
  pyramid file via new `worktree.BranchSHA`/`FileAtBranch` and `pyramid.Parse`, so
  the badge shows in exactly the column a reviewer looks at. Like `sessionDot`, any
  git/parse error degrades to no badge. The git reads sit behind an injectable
  `Server.pyramidGit` seam, exercised by mocked (`pyramid_mock_test.go`) and
  real-git (`pyramid_render_test.go`) render tests.
- **Downward resolution over `wants:` with `resolve --by`, and move-up escalation
  under pre-digest (drvctl-050).** Before this change, a coordinator added to the
  human's workload instead of absorbing it. A child escalation landed in Needs-me
  *and* woke the pre-digest parent, which escalated as well, so one problem made
  two board items. Resolving the parent also left the child blocked. The `wants:`
  edge now carries a third flow, **resolution, downward**. The new verb
  **`draiver resolve <CHILD> <seq> --by <PARENT>`** (`repo.ResolveBy`) lets a
  coordinator answer one of its child's escalations. It is authorised by the edge
  (PARENT must `wants:` CHILD), uses the same seq/open-escalation validation as
  plain `resolve`, and records a distinct `parent:<PARENT>` actor that shows up in
  `brief`/`log`. It only ever writes a `resolution`, so a parent can unblock a
  child but can never move it from Review to Done. If the parent is not
  authorised, the command fails and writes nothing. Escalation now **moves** up
  instead of being **copied**. Under pre-digest, a Needs-me child with an
  enabled, Running pre-digest wanter is shown as **Pending "escalated to `<P>`"**,
  credited to the parent and left out of the Needs-me count and `draiver inbox`.
  The mapping comes from `project.DeriveEscalated`, which uses the same predicate
  as the reconciler's wake path, plus `Attempt.EscalatedTo` /
  `Attempt.BoardState()`. If several tickets want the child, the lexically first
  one wins. Under passthrough the child stays Needs-me. This is purely read-time:
  `draiver status`, the web board and `inbox` change, but the child's log, its
  derived state and the reconciler's admission/wake behaviour do not.
- **Named test environments in `.test-pyramid.yaml` — up/down/healthcheck contexts
  a rung runs against (drv-012).** A test rung runs *somewhere* — locally, against
  staging, against QA — but a level was only a bare `run` command. The pyramid file
  gains a top-level **`environments:`** list, a sibling of `levels:`, of named
  `{name, up?, down?, healthchecks: [{name, script}]}` contexts, and a level gains
  an optional **`environment:`** naming the one it targets, so one staging context
  can be shared by several rungs. `up`, `down` and each healthcheck `script` are
  shell command strings run via `sh -c` exactly like `run:` — not paths to a
  script file. Unset means today's ambient context, so existing files keep working;
  `internal/pyramid` validates unique, non-blank environment names, non-blank
  healthcheck name/script, and that every level's environment resolves
  (`(*Pyramid).EnvironmentFor`). **`draiver test`** now runs a bound rung as up →
  healthchecks → run → always-down, mapping each failure to the control outcome
  `docs/test-pyramid.md` settled, via distinct exit codes: a failed `up` is an
  infrastructure fault (**exit 6**, `ExitEnvUp` — escalate), a red healthcheck is a
  dependency not ready (**exit 7**, `ExitBlocked` — block), a failed `run` is a
  code fault (exit 1); `down` runs unconditionally and best-effort (a failure only
  warns, never changes the verdict). The board's cleared-rung badge shows the
  rung's environment (e.g. `@local`); the last healthcheck verdict isn't logged, so
  it isn't shown. The repo dogfoods a `local` environment on its integration rung,
  and `docs/test-pyramid.md` folds the design in. Secret handling for environments
  is a follow-up.
- **Git controls on the attempt page — Push and Sync from the web UI
  (drv-013).** The attempt page gains a **Git** section with two buttons, shown
  for a non-Done attempt that records a base and repo. Each is a same-origin htmx
  POST (`/ticket/{id}/{attempt}/git/push`, `.../git/sync`) that returns the live
  log fragment plus an out-of-band result banner. **Push** goes through the new
  `worktree.Manager.Push`, draiver's first write to a remote: a plain `git push` of
  the attempt branch that never uses `--force`, so a non-fast-forward is rejected.
  **Sync** now brings trunk down in two steps: it fetches the default remote and
  fast-forwards the local base toward the remote tip (best-effort: with no remote it
  just back-merges offline, and a fetch or fast-forward fault is only a warning),
  then back-merges that base into the attempt branch. The two operations live in
  `land.Push` / `land.Sync`, each records a durable `note`, and `ctl sync` was moved
  onto `land.Sync` so the CLI and web share one write path. The remote comes from
  the existing `resolveRemote` (sole remote or config `primary_remote`) and the
  default branch from the attempt's recorded base, in place of drv-011's per-repo
  defaults. The old web **"Merged elsewhere — close & pull base"** button and its
  `POST .../merge-remote` route were **removed**; the CLI `ctl merge --remote` is
  kept. The spec's **Create PR / Merge PR** buttons were **not built**: they need a
  forge-API adapter and auth, which draiver has so far avoided, so they are left to
  a follow-up ticket.
- **Per-repo settings — default remote, branch and target test rung, stored in the
  global config and edited from the attempt page (drv-011).** Some knobs belong to
  a repo, not an attempt. `config.Config` (`~/.draiver/config.json`) gains a
  **`repos`** map keyed by repo path, each entry holding `default_remote`,
  `default_branch` and `default_test_rung`; values resolve **per-repo → global →
  built-in** (remote falls back to `primary_remote`) via `cfg.RepoSettings(path)`,
  and a pointer-mirror file struct keeps omitted-vs-zero intact on round-trip. A
  new atomic merging writer, `config.SetRepoSettings`, patches one repo's changed
  keys without clobbering anything else, exposed through `repo.Repo` so the web
  layer never touches `config.json` directly. Three surfaces use it: a
  **repo-settings panel** to the right of provenance on the attempt page
  (post-on-change, in-place swap, "Saved" flash, scope copy noting it applies to
  every attempt on that repo, disabled hint when the attempt has no repo); a new
  **`draiver config repo <repo> [--remote R] [--branch B] [--rung N]`** verb that
  prints the resolved values or sets them (an empty value clears a key); and the
  consumers — `draiver new` defaults `--base` from `default_branch` before falling
  back to the repo's current branch, bare `draiver test` climbs to
  `default_test_rung` when it names a real rung, and the Review gate applies it
  only as a **floor** (`pyramid.FloorTarget`) that can raise but never lower the
  bar. Config read errors are best-effort and never fail `new`, `test` or `review`.
- **BDD artefact capture: run evidence is stored per run and referenced from the
  log (drv-017).** A green `draiver test --log` run on a rung that declares
  evidence now copies it into the attempt's `artefacts/` store. The evidence is
  the rung's first-class `cucumber_json:` report (drv-016) plus any extra outputs
  listed under a new optional `artifacts:` field on `pyramid.Level`, such as
  embeddings dirs or script-output files. Each run goes under a per-run key,
  `bdd/<rung>/<env>/<commit>/<runstamp>`, so repeated runs at the same commit and
  environment sit side by side and never overwrite each other. Each set comes with
  a `run.json` manifest for reproducibility. The captured set is referenced from
  the run's `test-result` event (`artefacts:`), which puts the evidence on the
  hash chain: `draiver audit` covers it and `draiver brief` lists it. A rung that
  declares either `cucumber_json:` or `artifacts:` counts as a capture rung. The
  new `internal/bddartefact` package provides `Capture` and `Prune`. `Prune`
  applies retention per `(rung, env)` group under a new `bdd_artefact_keep` config
  knob; the default is `0`, meaning keep everything. Captured outputs are
  reproducible evidence, not source, and should be git-ignored so that repeated
  `--log` runs don't trip the dirty-tree guard (see `docs/test-pyramid.md`). The
  ticket was built before its drv-016 and drv-012 dependencies and then
  reconciled with them once they merged.
- **BDD execution rung — `draiver test` captures cucumber-JSON from the repo's
  configured runner (drv-016).** A rung in `.test-pyramid.yaml` that sets the new
  optional **`cucumber_json:`** field (the report path, relative to the worktree
  root) is a BDD/acceptance rung. Its `run` is the same generic `sh -c` shell-out
  every rung uses — whatever runner the repo configures (godog, cucumber-js, …);
  draiver never hardcodes a tool — and it binds an `environment:` for the drv-012
  lifecycle (`up` → healthchecks → `run` → `down`, with the existing
  escalate/block/code-fault outcome mapping unchanged). After the run, `draiver
  test` reads the emitted standard cucumber-JSON and prints a summary via a new
  **`internal/cucumber`** package (`Report`/`Feature`/`Element`/`Step`/`Embedding`
  model, `Summary()`, `Passed()`; lenient on unknown fields, strict that the top
  level is an array) — the runner-agnostic interchange that artefact storage
  (drv-017) and HTML rendering (drv-018) build on. Capture is best-effort and
  verdict-neutral, like `down`: it runs whether the rung passed or failed, and a
  missing or invalid report only warns — the `run` exit code stays authoritative.
  `docs/test-pyramid.md` gains a BDD-rung section.
- **Standalone cucumber HTML report — renderer, attempt-page embed, served routes,
  and a `draiver report` command (drv-018).** A new runner-agnostic renderer
  (`internal/report`) turns standard cucumber-JSON plus its embeddings into a single
  self-contained HTML file — inlined CSS/JS, base64-embedded screenshots — that opens
  offline, with a feature/scenario tree, per-step status, timings and tags. A
  `Resolve` hook switches to by-reference URLs for the webui so heavy embeddings
  stream as artefacts instead of bloating the page. The attempt page shows the latest
  run in a sandboxed iframe (`allow-scripts` only, since test output is untrusted),
  with an `<optgroup>` selector among side-by-side regenerations, hidden until a run
  exists. `GET …/report` serves the report (standalone, or by-reference with
  `inline=0`) and `GET …/report/asset` serves one embedding's bytes, with
  Content-Type forced to a safe image/text allow-list plus `nosniff` and
  path-traversal guards. Runs are discovered read-only under drv-017's contract key
  `artefacts/bdd/<rung>/<env>/<commit>/<runstamp>/…`, built against the spec because
  drv-017's capture had not landed yet. A follow-up adds `draiver report <TICKET>`,
  which renders the latest run (or `--run <rel>`, or an explicit `--json <file>`) to
  a standalone file (`-o`/stdout), and `--demo`, which seeds a representative run
  (with a screenshot) into the attempt's store so the embed can be reviewed before
  real capture exists.
- **Download and rerun BDD reports from the webui, plus a dogfood acceptance rung
  with real Playwright screenshots (drv-019).** The attempt page's BDD-report panel
  gains two actions. **Download** (`GET /ticket/{id}/{attempt}/report/download`)
  serves the self-contained standalone report (drv-018, screenshots inlined) as a
  file attachment with a sanitised, descriptive filename. **Rerun** (`POST
  /ticket/{id}/{attempt}/report/rerun`) delegates to a new in-process controlplane
  executor, **`internal/bddexec`** (the BDD counterpart to `internal/land` — the
  web layer never runs BDD tooling itself): it brings the attempt's bound
  environment `up` (drv-012), enforces the **healthcheck precondition** (a red
  probe blocks with the "pass the ball" message instead of producing a bogus
  report), re-executes the BDD rung, captures a fresh side-by-side artefact set,
  and refreshes the embedded report out-of-band. The handler follows the git
  controls' pattern — same-origin guard, live fragment plus OOB result banner — and
  a rerun only produces evidence; it **never clears the Review gate**. Rerun is
  whole-rung, not per-scenario. The repo also dogfoods BDD: `features/*.feature`
  Gherkin, **`internal/acceptance`** (a minimal Go Gherkin reader + step registry,
  no godog, emitting cucumber-JSON with PNG embeddings), a new **`acceptance`** rung
  in `.test-pyramid.yaml` between `unit` and `integration`, bound to a new `bdd`
  environment, and **`internal/webshot`**, a stdlib-only seam that drives the
  pre-installed chromium via Node Playwright (`features/screenshots`) so each
  scenario attaches a real screenshot of the live webui. A `playwright-browser`
  healthcheck blocks the rung when node/playwright/chromium are missing, and the
  unit rung skips the browser tests on a browserless box.
- **Launch a live review environment from a Review attempt (drv-020).** At
  Review, a reviewer can now stand up a running, seeded instance of the change and
  click around it instead of only reading the diff. The attempt page gets a
  **review-env** tab. It is offered only when the attempt is in Review, the repo
  setting **`review_environment`** (added to `config.RepoSettings`, editable in the
  repo-settings panel) names an environment, and that environment in
  `.test-pyramid.yaml` defines **`up` + `down` + at least one `healthcheck`**
  (`Pyramid.EnvironmentByName`, `Environment.ReviewReady`). A misconfigured
  setting fails loudly. Launching (`POST /ticket/{id}/{attempt}/review-env/up`)
  goes through the new **`internal/reviewenv`** lifecycle package, which the
  control plane owns. It cuts a detached review worktree at the reviewed commit,
  runs `up` with an injected `DRAIVER_REVIEW_INSTANCE` / `DRAIVER_REVIEW_PORT` so
  concurrent environments do not collide, and reveals the environment's `url` only
  after the healthchecks pass. Teardown happens through the **tear down** button
  (`.../review-env/down`) or a per-tick reconciler sweep that reaps environments
  whose attempt is Done or older than `review_env_max_age_minutes`. It runs
  `down`, removes the worktree, then re-runs the healthcheck to **confirm
  down**: a service that still answers is flagged as a leak. Per-environment state
  is persisted, and `.../review-env/status` serves the panel. Declared environment `params` (for
  example `ENV_NAME`) appear as editable inputs on the launch form. Only declared
  params are accepted, and the injected `DRAIVER_REVIEW_*` values always win. The
  repo ships its own qualifying `review` environment (`review-env/up.sh`,
  `down.sh`, `healthcheck.sh`). It builds and runs the real draiver web UI from the
  reviewed worktree over a dummy data dir (`.review-env-run/`, never
  `~/.draiver`) seeded with sample tickets. Covered by `features/review_env.feature`
  with live Playwright screenshots.
- **Git controls: Sync always reports an outcome, and the panel shows the current
  branch plus ahead/behind the primary remote (drv-022).** Hardens the drv-013 Git
  controls panel on the attempt page. **Sync banner:** a failing sync (dirty
  checkout, merge conflict, bookkeeping error) used to route through `s.fail` as a
  bare 500, leaving the panel blank; it now renders a failure-styled `git-result`
  banner carrying the error at HTTP 200, and a sync where nothing moved renders an
  explicit "Already up to date — nothing to pull or back-merge." banner instead of
  an empty one. The `git-result` partial now takes a `gitResultVM{Kind,Message}`
  with distinct `ok`/`noop`/`fail` styling; genuine server faults still go through
  `s.fail`, and Push is unchanged. **Position line:** the panel now shows the
  branch actually checked out in the worktree (or base repo) and its standing vs
  the primary remote — `up to date` or `ahead N · behind M` — with the remote
  resolved exactly as Sync does (sole remote, else config `primary_remote`). The
  read is a new `worktree.AheadBehind` (`git rev-list --left-right --count`,
  guarded by a tracking-ref check) behind an injectable `s.gitPos` seam surfaced as
  the `gitPosition` template func, so the 3s live poll never triggers git I/O; no
  remote, no upstream, or a detached HEAD degrade to "unknown" without breaking the
  panel or the Sync POST. BDD coverage with Playwright screenshots of all three
  Sync outcomes lives in `features/git_controls_sync.feature`.
- **Navigable BDD report: tag filter, `Rule:` groups, subsequence filter box and
  group-by (drv-023).** Reports with many features can now be narrowed in seconds.
  All of it ships through the shared renderer `internal/report` as inlined vanilla
  JS, so both the standalone download and the webui iframe get it, and the file still
  opens offline. Tag chips and a new facet bar (every tag in the run with its
  scenario count) toggle one shared selection with OR semantics; tags are the
  effective, inherited set (feature ∪ rule ∪ scenario), features whose scenarios are
  all hidden hide themselves, and matches auto-expand. Gherkin `Rule:` is parsed
  leniently from a per-element `rule` reference and rendered as a labelled
  `<details class="rule">` sub-group with its own status roll-up; scenarios without
  one render flat as before. A free-text box does case-insensitive, in-order
  subsequence matching over a per-scenario key (feature URI/folder, feature, rule,
  scenario, tags) and combines (AND) with the tag filter. A group-by control offers
  folder (default, from the URI directory, server-rendered), tag, status
  (worst-first) and flat. Covered by `internal/report/navigation_test.go` and
  `TestReportRouteShipsNavigation` in `internal/web`.
- **Per-rung `required` in `.test-pyramid.yaml` — advisory rungs the review gate
  does not require (drv-025).** The pyramid used to fuse *altitude* (list order on
  the testing V) with *enforcement* (the file's top rung was the review gate), so
  the only way to make a flaky or environment-dependent rung non-blocking was to
  delete it or reorder it. A level now takes **`required: true|false`** (default
  `true`, so existing files behave exactly as before), orthogonal to order: an
  advisory (`required: false`) rung is still defined, run when it lies in the climb
  path and reported, but a red advisory rung never blocks review, and a required
  rung may sit above an advisory one. A pyramid with zero required rungs is
  rejected at load. **The review gate now folds over the required set** —
  `draiver review` is admitted only when *every* required rung is logged green at
  the exact HEAD (`pyramid.RequiredStatus`), and a refusal names the outstanding
  rung(s); `HighestAtHEAD` now only drives the web badge. **`draiver test`** halts
  only at the first non-green *required* rung (advisory reds are recorded and the
  climb continues), and bare `draiver test` climbs to the **highest required rung**
  rather than the file's top; an explicit `RUNG` still climbs into advisory rungs.
  This supersedes drv-011's floor semantics: the repo `default_test_rung` /
  `TestRung` setting no longer touches the gate (`FloorTarget` is gone) and is now
  purely the iteration ceiling that lowers bare `draiver test`'s climb. The repo's
  own `integration` rung becomes `required: false`, so its gate is `unit` +
  `acceptance`; `docs/test-pyramid.md` and
  `features/test-pyramid/required-gate.feature` cover the new semantics.
- **Progressive-reveal brief — `supersedes`, a headline index, and `draiver
  recall` (drv-026).** `brief` used to replay the entire attempt log verbatim, so
  every cold start (and every respawn) cost O(total log). The brief is now a
  deterministic fold over the immutable log (new `internal/kb` package). Instead of
  the full dump it prints a bounded **`## Current understanding (N live facts)`**
  index, one headline (the first body line) per live fact, plus open escalations,
  so its size grows with the number of live facts rather than the event count.
  Events gain two `omitempty` fields, so existing hashes and `audit` are unchanged.
  **`supersedes`**, set with `draiver log --supersedes SEQ`, retires an earlier fact
  from the live set. **`tags`**, set with `draiver log --tag`, are opaque labels the
  agent owns and draiver never interprets. The new pull side, **`draiver recall
  TICKET [SEQ...]`**, prints full bodies. `--chain SEQ` shows a fact's supersession
  history oldest to newest, and `--type` / `--tag` / `--grep` / `--all` filter the
  index. Attempts from before this change still brief and recall correctly, with
  every event treated as live. On the board, superseded events are demoted with a
  link to the event that replaced them, and tags show as chips. The onboarding skill
  (and the embedded `internal/handbook/handbook.md`) teaches the
  brief/recall/supersede loop and includes a runnable `claude -p` example for
  "talking to an attempt" out of band through `recall`. Covered by
  `features/progressive-brief/`.
- **"Restart context window" button on context-window auto-stop escalations
  (drv-028).** When the limit gate auto-stops an attempt because its context
  window filled, the agent is not waiting on a human answer, so resolving and then
  restarting by hand was two steps for the common case. The gate now tags its
  escalation with `context-auto-stop` (`limit.TagContextAutoStop`), so the board
  recognises it without sniffing prose, and an open escalation of that kind renders
  a second action beside Resolve: **Restart context window**. It posts to the new
  **`POST /ticket/{id}/{attempt}/restart-context`**, which first resolves the
  escalation (the durable half), then restarts the attempt on a clean context
  window — the `ctl restart --new-session` path (`reconcile.Restart` with
  `FlushSession`), injected via `web.WithRestart` from `cmd/webui.go`. A restart
  needs a running `ctl up`; if it cannot be scheduled the escalation still resolves
  and an out-of-band `restart-result` banner reports the failure. The response swaps
  the live log region like Resolve does. Ordinary, permission and already-resolved
  escalations do not show the button. BDD coverage in
  `features/attempt-page/restart-context.feature`.
- **`draiver park` — suspend an attempt to Pending without escalating
  (drvctl-052).** A third legitimate way for an agent to end its turn, alongside
  `review` and `escalate`: **`draiver park TICKET [REASON]`** appends a durable
  `park` turn-end and exits 0, leaving the attempt **Pending** (enabled, no live
  agent, self-resolving) with nothing on the human's inbox. `park` is a
  non-lifecycle event, so the log still derives `Running` and the existing
  `Control` fold projects it to Pending once the session is reaped; the log and
  attempt persist and a later wake resumes from `brief`. It **refuses** (nonzero,
  nothing appended) when the attempt has no pending wake source, since it would
  otherwise sleep forever. Wake sources sit behind a new abstract seam,
  `internal/wake` (`wake.Pending`); the only source today is the coordinator one —
  a `wants:` ticket with at least one non-Done child — with async jobs left for
  drvctl-055. The completion gate (`internal/completion`) now folds the wake check
  in: a `park` counts as a hand-off only when the attempt is parkable, and an
  unmet success turn on a parkable attempt gets a new **`Parked`** outcome instead
  of an escalation — fixing the case where a `wants:` coordinator that delegated
  via `resolve --by` (which writes to the child's log) and stopped was escalated
  onto its own ticket. Non-parkable leaf behaviour (nudge, then escalate) is
  unchanged; the existing coordinator gate and reverse-`wants:` withdrawal retire
  the parked attempt to Pending, and `internal/watch` promotes `draiver park` into
  the supervised log like `review`/`escalate`. Design:
  `docs/manager-as-worker.md`.
- **Generalised manager activation — child lifecycle doorbells, the `woken-by`
  event, and serial queue/replay (drvctl-051).** Reverse-`wants:` activation in
  `internal/reconcile/reverse_wants.go` (drvctl-041's escalation-only, level-
  triggered wake) is rewritten into the manager-as-worker wake from
  `docs/manager-as-worker.md`. A wanted child's **escalation, review, or done**
  event (the `doorbellTypes` trigger set) rings an **edge-triggered doorbell** — a
  transition, so a child merely *sitting* in Review never re-wakes its manager. When
  the spine wakes a pre-digest, enabled manager it first appends a durable
  **`woken-by` event** to the *manager's own* log (body `woken by <child> #<seq>`,
  plus new omitempty `woken_child`/`woken_seq` fields on `event.Event` that enter
  the hash only when set, so existing events hash as before). That one entry serves
  as dedup record, replay ordering, and the newest line of the manager's cold-start
  `brief`. The **queue** is derived — doorbells with no matching `woken-by` — and is
  drained **oldest-first, one doorbell per wake**: a manager with an activation and
  no later `park` stays desired while it handles it; once it `park`s it is reaped
  to Pending and re-admitted for the next doorbell. A manager with its **own open
  escalation is never woken**; its doorbells accumulate and replay resumes once the
  human resolves it. Behaviour change: a woken coordinator now reconverges to
  Pending on its own `park`, not when the child's escalation resolves (drvctl-044's
  test migrated). The staleness watchdog and StartLimit loop-safety were deferred
  to follow-on tickets.
- **Manager brief — a `wants:` ticket is told it is a coordinator
  (drvctl-053).** `brief.Build` (`internal/brief`) used to produce the same brief
  for every ticket and stripped spec frontmatter, so a `wants:` coordinator could
  not see its own children and fell back to implementing its spec itself — the one
  thing a manager must not do. The brief now branches on whether the ticket has
  `wants:` edges. A manager's brief gains a **`## Your team`** roster (each child's
  control state, from the `project.Control` fold over its latest attempt, plus that
  child's open escalations); the **coordinator protocol** (your spec is a charter,
  not a work item; assess; `resolve --by` / `log --by` what you can; escalate
  **once**, consolidated, only when a human is really needed; then `park`); and the
  **wake reason** — the `woken-by` activation event from drvctl-051, rendered as the
  freshest tail of the brief. A ticket with no `wants:` edges gets the worker brief
  unchanged. This reverses the "no special brief" line in
  `docs/capabilities-and-supervision.md` (design in `docs/manager-as-worker.md`).
  Covered by `features/progressive-brief/manager-brief.feature` and unit tests.
- **`draiver log --by <parent>` — edge-authorised cross-ticket log (drvctl-054).**
  The `log` sibling of `resolve --by` (drvctl-050): a coordinator can push durable
  direction or context into a child it `wants:` — enabled children + `log --by`
  (direct) + `resolve --by` (unblock) is how a manager assigns work. The write is
  authorised by the edge itself: `Repo.LogBy` in `internal/repo` checks the parent's
  `spec.md` `wants:` names the child and otherwise refuses with "does not want … not
  authorized", writing nothing. Accepted facts land in the child's log stamped with
  a `parent:<P>` actor. The bright line is enforced in the gateway, not the caller:
  `log --by` may only write a fact (`gotcha`/`decision`/`note`/…), never `review`,
  `done` or another verb-owned lifecycle event — direct is not ratify, so a parent
  can never move a child Review→Done. The type check runs before the edge load, so a
  forbidden type is refused without touching the data root.
- **Park pending your own async work — detached `draiver test --detach` plus
  worker park/wake (drvctl-055).** A worker running a long test used to hold a live
  agent idling (context and token burn) or stop with no handoff, which the
  completion gate turned into a **false escalation** a human had to discern and
  resolve. The pattern is now `draiver test --detach && draiver park`:
  **`--detach`** requires a clean tree, writes a new **`async-job`** launch marker
  on the attempt's own log, spawns a `setsid`-detached runner (the hidden
  `__detached-test` verb, logging to `detached/test-NNNN.log`) and returns at once;
  when the climb finishes it lands a durable **`test-result`** carrying a new
  `Outcome` (`pass`/`fail`) and an artefact pointer. `wake.Pending` gains an
  **`AsyncJob`** source, so the parked worker shows as **Pending**, not Needs-me.
  A new reconciler pass (`internal/reconcile/async_jobs.go`,
  `reconcileAsyncJobs`) holds it Pending while the job is in flight and, when the
  result lands, authors a `woken-by` (reusing drvctl-051's path activation, with
  the worker's own ticket as the woken child) to cold-start it with the result as
  the brief tail — to claim review on green or iterate on red. `launch && park` is
  safe: a failed launch short-circuits `&&` (and answers its own marker), and
  park's no-wake-source refusal (drvctl-052) backs it up. As a safety net, the
  completion gate **auto-parks** a handoff-less turn-end when a live async job
  exists instead of escalating. Retire now keeps the worktree warm while an
  `AsyncJob` is in flight (the detached climb runs there), and `draiver review`'s
  green gate ignores `Outcome: fail` results. The handbook and onboarding skill
  gain a new §8 documenting the `launch-async && park` pattern; BDD coverage lives
  in `features/manager-as-worker/park-async.feature`.
- **Manager signal on the board — coordinator cards show their wake reason and
  queue depth (drvweb-023).** A running manager used to look like any running
  ticket, and its Pending→Running→Pending oscillation was invisible. Now a ticket
  carrying `wants:` edges renders as a distinct **coordinator card**
  (`card-coordinator`, a green border) whose line surfaces the control plane's own
  recorded reason: "manager — on it, re: `C1`" from its latest `woken-by`
  activation event (drvctl-051), plus "· N queued" for child doorbells that have
  no activation yet, or "manager — idle" when there is no active wake. The signal
  is derived in the `project` package (`DeriveCoordinatorSignal`,
  `internal/project/coordinator.go`) rather than by importing the reconciler. The
  Capability panel on the detail page (drvweb-017) gains the same line for the
  manager's **own** current action, not just its children's states. Also fixes a
  board bug where a Pending card's `WaitingReason` such as "escalated to `P`"
  showed literal backticks: it (and the Capability panel's gate reasons) now
  renders through the `inlineMD` markdown pipeline. Acceptance coverage in
  `features/manager-signal/board.feature`; a follow-up commit points its
  screenshots at the full board page `/` rather than the unstyled `/board`
  partial.
- **`draiver park --clear` — the operator inverse of park, force-resolving an
  attempt's pending wake source (drv-031).** When the thing meant to wake a parked
  attempt never fires (e.g. a detached `draiver test` runner dies), the only escape
  used to be hand-forging a `test-result` with `draiver log` — unsafe, because an
  empty `Outcome` reads as legacy-green at the review gate. `--clear` reads
  `wake.Pending` and dispatches a typed switch over `wake.Kind`: an `async-job` gets
  a terminal `test-result` answering the open launch marker (`Outcome: fail`,
  `Refs: [<launch seq>]`, `Rung` copied, a body saying an operator cleared the
  wait), so the existing async pass wakes the worker and the gate never counts it
  green; a `wants` coordinator gets a manual `woken-by` past the park so it is
  admitted to assess now; an unknown kind errors rather than silently no-op. With no
  pending source it prints `nothing to clear: …` and exits 0, writing nothing. Events
  carry operator provenance, and no new lifecycle event is introduced. The
  terminal-result shape is factored into `wake.AsyncLaunchFailResult`, shared with
  `launchDetached`'s spawn-failure path (and a future reconciler reap). `park` in
  both modes now accepts `<ticket[@attempt]>` — the suffix wins over `--attempt` —
  via `resolveCtlTarget`, renamed to the neutral `resolveTarget`. Covered by
  `cmd/park_test.go` and a BDD scenario in
  `features/manager-as-worker/park-async.feature`.
- **`recall` gains `--tags`, the `ticket@attempt` address form, and a header naming
  the resolved attempt (drv-032).** `draiver recall <ticket> --tags` lists the
  distinct tags of the in-scope facts, sorted alphabetically with a per-tag fact
  count; it honours `--type`/`--tag`/`--grep`/`--all`, refuses (writing nothing)
  alongside positional seqs or `--chain`, and prints `(no tags)` when empty. The
  positional ticket now goes through the shared `ticket[@attempt]` parser, so
  `recall drv-030@0001` (and `… 3 4`, `--chain`, `--tags`) targets that attempt;
  an explicit `@attempt` wins over `--attempt`/`$DRAIVER_ATTEMPT`, and a missing
  ticket or attempt errors. Recall no longer hides sibling attempts: a
  `# <ticket>@<attempt>` header goes to **stderr** (stdout stays clean for pipes),
  extended with `— latest of N attempts (draiver attempt ls <ticket>)` when the
  attempt was picked implicitly and others exist. Usage now reads
  `recall <ticket[@attempt]> [SEQ...]`, matching the `ctl` verbs; `brief` keeps
  `TICKET` as it does not accept `@attempt` yet. For review, a sandboxed docker
  **ttyd** web terminal (`review-env/ttyd/`, declared as the `review-cli`
  environment in `.test-pyramid.yaml`) serves a seeded multi-attempt `DEMO-033`
  ticket in the browser. Covered by `cmd/recall_test.go` and
  `recall-attempt-aware.feature`.
- **Multiple review environments per attempt, each launched and torn down
  independently (drv-034).** The attempt page used to offer only the one environment
  named by the per-repo `reviewEnvironment` setting, so a pyramid declaring both
  `review` (webui) and `review-cli` (ttyd terminal) could only ever run one. It now
  renders one panel for **every** `ReviewReady()` environment the pyramid declares,
  each with its own Launch / Open / Tear-down controls and params, and they can run
  concurrently. Handlers are scoped by path segment — `POST /review-env/{env}/up`,
  `/{env}/down`, `GET /{env}/status` — and a name that is not a declared,
  review-ready env is rejected with a 400 that writes nothing. Run state is keyed
  per `(attempt, environment)` (`store.ReviewEnvPath(id, attempt, env)` →
  `reviewenv-<slug>-<hash6>.json`), with dual-read fallback to the legacy
  `reviewenv.json` so existing attempts keep their env. Isolation goes further than
  the record: the `DRAIVER_REVIEW_INSTANCE` slug and the detached review worktree
  path both include the env, so tearing one down never disturbs a sibling. The
  per-repo `reviewEnvironment` is demoted from a gate to an optional default,
  shown as a `· repo default` badge, and still raises the loud misconfiguration
  note when it names a missing or non-review-ready env. BDD scenarios in
  `features/attempt-page/review-env.feature` (`@drv-034`) capture both the
  offered-to-launch and two-running-at-once states with screenshots.
- **`recall` instrumentation and `draiver recall-stats` — measure belief
  utilisation (drv-036).** `draiver recall` now records one JSON line per
  invocation (ticket, attempt, read mode, and the seq(s) surfaced) to a root-level
  `recall-metrics.jsonl` sink, and a new **`draiver recall-stats [ticket]`** folds
  that sink into per-ticket and per-fact recall counts, most-recalled first — the
  baseline for seeing which logged facts agents actually pull back. The sink lives
  in the new stdlib-only `internal/recallmetric` package (`Append`/`Read`/
  `Aggregate`; path via `store.Root.RecallMetricsPath`) and sits **outside the hash
  chain** — observability, not truth. Recording is best-effort with errors
  swallowed, so it never changes `recall`'s output or exit code; `--tags` queries
  and error paths record nothing. Acceptance scenarios in
  `features/recall-instrumentation/baseline.feature` (`@drv-036`).
- **Ticket `type` field — `ticket` (default) or `codebase`, a belief ledger that
  rests Parked and never reaches Done (drv-037).** `spec.md` frontmatter gains an
  optional `type:`; absent means `ticket`, so every existing ticket behaves and
  serialises byte-identically. `draiver new X --type codebase --repo <path>`
  authors a codebase ledger (writing `type: codebase`; an ordinary ticket gets no
  line), and `recall`/`log` work on it with no new flags. Unknown types are
  rejected both at `new --type` (validated before any side effect, so no orphan is
  written) and at spec load. A type-aware overlay on the control fold in
  `LoadAttempt` collapses a ledger's resting `Running` and its `Done` to `Pending`
  (Parked), while Needs-me and Review pass through — so a ledger has no Done
  transition, can still be Archived, is absent from the Running/attention totals,
  and surfaces only via the normal state machine (e.g. an open escalation). Per the
  human's resolution the default type is named `ticket` rather than the spec's
  `code`, and no enable-refuse gate was built: a Parked, un-enabled ledger is never
  admitted by the reconciler by construction. Conceptual backing in
  `docs/belief-store.md`; BDD coverage in `features/codebase-ledger/type.feature`.
- **Board renders codebase ledgers apart, behind a Code/Ledgers view toggle
  (drv-039).** The web-UI half of the codebase-ledger type (drv-037). The board
  takes a `?view=code|ledgers` filter with a two-link toggle: the default code view
  shows code tickets plus any ledger in an attention state (Needs-me/Review), so a
  resting ledger stays out of the way but an escalated one still surfaces through
  the normal Stuck path; the ledgers view shows only ledgers. The 3s poll URL
  carries the view, and the archive re-render keeps it via `HX-Current-URL`.
  Ledger cards carry a "ledger" marker and a "parked — codebase ledger" rest label,
  never get a play control (`canEnable` now excludes codebase explicitly), and
  since a ledger is never Done they only ever offer the archive close-cross. Facts
  reuse the attempt page's existing log timeline rather than a bespoke view.
  Favicon attention reads a global Stuck/Review tally, so switching views never
  hides a code ticket's signal. `review-env/up.sh` seeds a resting and an
  escalated ledger. While adding Playwright screenshots to the BDD scenarios
  (`features/codebase-ledger/web.feature`) this also fixed a pre-existing bug
  affecting every ticket's BDD report: screenshots were `loading="lazy"` images
  inside closed `<details>`, so none ever loaded — image embeds now render in an
  open `<details>` without lazy loading.
- **Cold-start brief points agents at their repo's codebase ledger, and the
  handbook teaches writing back to it (drv-038).** `brief X` now resolves the
  `type: codebase` ledger(s) bound to X by repo — every non-archived ledger whose
  defining attempt `repo` matches X's (`filepath.Clean`ed;
  `project.CodebaseLedgersFor`) — and renders a short pointer section telling the
  agent to `recall` it like a library. Facts are pulled on demand, never inlined,
  so cold-start cost is flat; with no ledger bound there is no section and no
  behaviour change. Several matches are all listed rather than treated as an
  error. `draiver log` now accepts the `<LEDGER>@<attempt>` address form (shared
  `resolveTarget`, symmetric with `recall`), so an agent records codebase-level
  findings into its own scoped perspective attempt on the ledger. The handbook and
  onboarding SKILL (kept byte-identical) gain the read-at-cold-start instruction and
  a ticket-local vs. codebase-level write rule. The belief-store conceptual essay
  was merged into `docs/belief-store.md`. BDD coverage in
  `features/codebase-ledger/cold-start.feature`.
- **Named attempts — `draiver attempt new --name <slug>` (drv-040).** An attempt
  can now be created with a slug as its id instead of the next auto-number, so its
  address carries provenance — e.g. `repo-facts@drv-001` for the perspective
  drv-001 contributed to a codebase ledger. The name is applied at the single
  `attempt.Create` chokepoint (on-disk dir, genesis event, `attempt.md`), so a
  named attempt is first-class everywhere: `<TICKET>@<slug>` resolves in
  `log`/`recall`/`ctl`/`archive`, it passes `draiver audit`, and it renders on the
  board like a numbered one. `brief` was the one address-taking command that did
  not split on `@`; it now goes through the shared `resolveTarget` too. A
  colliding name is refused and writes nothing (pre-check plus the exclusive
  `os.Mkdir` claim), and a slug containing a path separator, `@`, whitespace or
  `.`/`..` is refused before any write. Omitting `--name` auto-numbers exactly as
  before — `nextID` ignores non-numeric ids. `--name` composes with
  `--from`/`--repo`/`--base`/`--model`/`--tool`. Known quirk: a bare `recall
  <TICKET>` on a ticket mixing numbered and named attempts resolves to the
  lexically last id, which is the name.
- **Test-pyramid overlays from a separate, developer-owned repo, pointed at only
  via settings (drv-041).** A solo developer whose team has not adopted draiver can
  keep `.test-pyramid.yaml` and the `features/`, review-env scripts and unit tests
  it drives in their own git repo, with nothing added to the target repo's version
  control. `internal/overlay` resolves the pyramid in order: an in-repo
  `.test-pyramid.yaml` wins (today's behaviour, unchanged); else an explicit
  per-codebase `overlay` on `repos.<path>` (twin of `review_env`); else
  `<overlay_dir>/<codebase basename>` from a new global `overlay_dir` key in
  `config.json`; else no pyramid and `draiver test` stays a no-op. The overlay gets
  its own per-attempt worktree on `draiver/<ticket>/<attempt>`, never touching its
  main, and runs keep cwd = the codebase worktree, exporting
  `DRAIVER_CODEBASE_DIR`/`DRAIVER_OVERLAY_DIR` only when an overlay is active.
  Attempt provenance records the overlay repo and exact commit. New
  **`draiver test create-overlay`** scaffolds a `features/` + `review-env/`
  skeleton, `git init`s and registers it — idempotent, refusing when an in-repo
  pyramid or overlay already exists. `draiver review` adds an advisory second
  compare URL for the overlay branch (or a branch + local-diff hint when there is
  no usable remote), warning loudly on an unmerged overlay change but never gating.
  The web UI edits `overlay`/`overlay_dir` from the repo-settings panel, shows the
  overlay provenance on the attempt header, and the pyramid badge falls back to
  the overlay's rungs. Documented in `docs/test-pyramid.md`; BDD coverage in
  `features/test-pyramid/overlay.feature`.
- **Promotions — a `promotions:` pyramid section, a `draiver promote` verb, and
  per-environment promoted labels on Review cards (drv-043).** draiver now
  *verifies*, never performs, environment promotions, on a separate informational
  axis parallel to test results. `.test-pyramid.yaml` gains `promotions:` entries
  (`environment` naming a declared environment, optional descriptive `base`, and a
  required `qa` command whose exit 0 means promoted); `validate()` rejects an
  undeclared environment, a blank `qa` or a duplicate, an absent key is a no-op,
  and `draiver test` never climbs a promotion. **`draiver promote <env> [--log]`**
  (`internal/promote`) runs the environment's up → healthchecks → `qa` → down,
  exporting `DRAIVER_PROMOTE_{ENV,BASE,URL}`, with exit codes matching `draiver
  test`; `--log` appends one commit-bound `promotion-result` event (new
  `Environment` field on `event.Event`) on a green run against a clean tree. A
  `DerivePromotions` fold feeds Review-column chips — green `dev ✓` when the pass
  is at HEAD, muted `dev ·` when the code has moved since, `dev ✕` on failure —
  and a Review-gated per-environment **Check** button on the attempt page (`POST
  .../promote/{env}`), which runs the check in a detached worktree at the reviewed
  commit and records pass *and* fail verdicts. Promotions never gate review or
  done. `--detach` was deferred (the async-job wake machinery is
  `test-result`-shaped) and the web button supports in-repo pyramids only. This
  repo dogfoods `local` and `forgejo-main` promotions, the latter checking the
  branch is merged via `$DRAIVER_PROMOTE_BASE`; `review-env/up.sh` seeds a green
  and a stale label; BDD coverage in `features/promotions/` with screenshots.
- **Promotions declared in a test-pyramid overlay now work from the board and the
  CLI alike (drv-044).** Promotions and overlays (drv-041) previously did not
  compose: only `draiver promote` honoured an overlay-only `.test-pyramid.yaml`.
  Three gaps are closed. `draiver config repo --overlay` now sets, clears and reads
  a repo's explicit overlay, mirroring `--remote`/`--branch`/`--rung` and writing
  byte-for-byte what the web repo-settings panel writes. The overlay resolution the
  CLI `promote`/`test` already did is factored into a shared
  `overlay.ResolvePyramid`, and the attempt-page promotion executor (`promote.Run`)
  now goes through it, threading the overlay's script env into the `qa` run — so an
  overlay-only promotion runs from the **Check** button instead of returning "no
  promotion". The no-promotion case (no in-repo pyramid and no resolvable overlay)
  is unchanged. The board path also records overlay provenance
  (`Overlay`/`OverlayCommit`) in the same shape as the CLI. Covered by
  `features/overlays/promotions.feature` (`@drv-044`) with a Playwright screenshot of
  an overlay-only promotion going green, plus `ResolvePyramid` unit tests.
- **System Logs panel on the attempt page, split out of Agent Logs and stamped with
  real event times (drv-045).** Agent Logs used to blend the agent session stream
  with draiverctld health lines and synthetic `session online` / `turn end`
  lifecycle lines, and stamped system lines with render-time `now()`. A new
  per-attempt journal, `session/system.jsonl` (`internal/systemlog`; best-effort,
  rebuildable, outside the hash chain), records system events stamped when they
  happen. A second streaming **System Logs** panel
  (`GET /ticket/{id}/{attempt}/system-logs`) tails that journal plus `ctl.jsonl`
  health, always rendering each record's recorded timestamp. Agent Logs now shows
  the agent stream alone. Events are now emitted for provenance edits, git
  push/sync, review-env up/down and promotion checks, and reconcile records
  `ctl-session` (admit) and `ctl-retire` (retire/park) lifecycle events. The split
  is UI-only: `TailStream`, `draiver ctl logs -f`, on-disk `stream.jsonl` and
  `--json` output are unchanged. BDD coverage in
  `features/system-logs/system-logs.feature` (`@drv-045`) with screenshots;
  `review-env/up.sh` seeds `DUMMY-2/0001` with a populated System Logs panel.
- **Interactive promotion controls on the board card: click a chip to run its check,
  and mark an all-green ticket Done from the card (drv-046).** The board's promotion
  chips were read-only and only appeared for environments that already had a
  result. Now every declared promotion gets a chip — grey `○` until first run, then
  green/red — and each chip is a button that posts the existing promote route with
  `?surface=card`. `handlePromote` re-renders the whole card, so the chip flips in
  place, with an inline spinner and `aria-busy` while the check runs. When a Review
  card has at least one promotion and all are green at HEAD (`canMarkDone`, ledgers
  excluded), a **mark done** tick appears; it posts to a new
  `POST /ticket/{id}/{attempt}/done`, which re-checks the gate server-side (400 if
  not all green) and moves the card to Done. It is separate from the Done column's
  archive tick. Neither control asks for confirmation. A "check started" system
  event before each run gives System Logs (drv-045) a timed started → pass/fail
  sequence. The chips move out of the card's `<a>` into a `.card-promos` strip at
  the card's foot, because a button cannot nest inside a link. A
  follow-up adds an amber **Restart context window** button on cards whose attempt
  auto-stopped on a full context window, which restarts it from the board without
  confirmation. BDD coverage in `features/promotions/board-controls.feature`
  (`@drv-046`) with screenshots; `review-env/up.sh` seeds a mixed grey/green/red card
  and an all-green card with a live mark-done tick.
- **`draiver test --only RUNG` runs and captures a single rung on demand, and the
  board's Rerun works for overlay-only codebases (drv-047).** `--only RUNG` runs
  just that rung, with its own environment setup and teardown and none of the rungs
  below it. It captures the rung's BDD report artefacts whatever the verdict, but
  writes **no** `test-result` event, so it produces evidence without touching the
  review gate. It exits with the rung's own status, runs on a dirty tree, and is
  refused alongside `--log`. It finds overlays through the same pyramid resolution
  as the full climb. The board's **Rerun** (`internal/bddexec`) now handles
  overlays (drv-041) too: `BoundBDDRung` reads an overlay-only codebase's pyramid
  in place, so the Rerun button now appears for those codebases, and `Rerun`
  resolves its rung through the shared `overlay.ResolvePyramid`, passing
  `DRAIVER_CODEBASE_DIR`/`DRAIVER_OVERLAY_DIR` to every step. An in-repo rerun
  is unchanged. BDD reports were already viewable outside Review (no state gate);
  a regression scenario now locks that in. Covered by
  `features/single-rung-reports/*.feature` (`@drv-047`) with screenshots.
- **Favourite star on board cards floats a ticket to the top of its column
  (drv-050).** Every card gets a star, revealed on hover beside the archive X and
  styled to match it; a favourited card's star stays visible. Clicking the star
  (`POST /ticket/{id}/{attempt}/star`) moves the card to the top of whatever column
  it is in; clicking again removes it. Several favourites sort above everything
  else, alphabetical among themselves, and the rest keep the default alphabetical
  order. Favourites belong to the person viewing the board, not the ticket, so they
  live only in a `draiver_faves` cookie (`ticket/attempt` refs, one-year lifetime,
  capped at 200 refs / 4 KB). The server stores nothing and the star writes no
  ticket event. Malformed, oversized or stale cookie entries are dropped when the
  board is read, falling back to alphabetical order. `applyFaves` (in
  `internal/web/favourites.go`) runs on every render of the board or a single card,
  so the order and the star survive the 3s poll and every in-place update, in both
  the code and ledgers views. Covered by `internal/web/favourites_test.go` and
  `features/favourite-cards/favourite.feature` (`@drv-050`) with real-browser
  screenshots.
- **Automatic liveness-reap for async jobs — a parked worker whose detached test
  runner died is woken instead of sleeping forever (drvctl-057).** A worker that
  runs `draiver test --detach && draiver park` (drvctl-055) waits on the detached
  runner to land a `test-result`; a runner that spawned but then died or hung
  (SIGKILL, a `timeout` wrapper, OOM, host reboot, a rung wedged on a server that
  never comes up) landed nothing, and the reconciler re-trimmed the attempt to
  Pending every tick indefinitely — until an operator ran `draiver park --clear`.
  Now `reconcileAsyncWorker` (`internal/reconcile/async_jobs.go`), in its "parked
  behind an unanswered launch" branch, checks whether the `async-job` launch is
  older than a deadline and, if so, lands a terminal **`fail`** `test-result` via
  `wake.AsyncLaunchFailResult` (refs the launch seq, spine actor, a body stating no
  climb ran to a verdict). The existing `woken-by` path wakes the worker on the
  next tick with no wake/activation code change; `Outcome: fail` keeps the review
  gate from reading it as green. The deadline is a launch-timestamp check, not PID
  liveness, so it survives reboots: new config key **`async_job_reap_minutes`**
  (default `30`, ~6x a normal acceptance climb; `0` disables) maps to
  `reconcile.Options.AsyncJobReapMaxAge`, mirroring `review_env_max_age_minutes`.
  At most one result lands per launch, and a racing real result still wakes the
  worker exactly once. The `wake.go` docs now list all three `AsyncLaunchFailResult`
  callers (manual `park --clear`, spawn failure, automatic reap); acceptance
  coverage in `features/manager-as-worker/async-reap.feature`.
- **Flow-metrics grid on the board, with an age-sort toggle on Oldest
  (drvweb-027).** A borderless 2x2 grid floats at the top right of the board, level
  with the Draiver header, showing four flow metrics, each with a hover `(i)` info
  icon: **Oldest** (age and name of the oldest unresolved code ticket), **Expected
  Dev-Cycle** (p85, nearest-rank, of resolved created→done durations), **Total
  Unresolved**, and **4-Week Throughput** (tickets done within the last 28 days).
  The metrics (`internal/web/metrics.go`) are a pure projection over code tickets —
  codebase ledgers are excluded — computed against an injectable clock (`s.now`) so
  they are unit-testable; the grid lives inside the polled `/board` region so the 3s
  poll refreshes it, and falls back to static flow on narrow screens. A ticket that
  reached Done and was then archived still counts as resolved (Dev-Cycle,
  Throughput); only an archived ticket that never reached Done counts nowhere.
  Clicking **Oldest** toggles a per-viewer age sort: `POST /board/sort` flips a
  `draiver_sort` cookie read on every render, so the order survives the poll.
  `applyFaves` became `applyBoardOrder` — favourites still sort first, then within
  each partition the age sort orders oldest-created first (else alphabetical), so it
  composes with drv-050 favourites. The pressed state shows as an accent-tinted,
  underlined tile. BDD coverage lives in `features/flow-metrics/flow-metrics.feature`
  (`@drvweb-027`) with screenshots.

### Changed

- **`ctl logs -f` tails the last 50 records instead of replaying the whole history
  (drvctl-030).** Follow mode used to replay an attempt's entire `stream.jsonl`
  from byte 0 before going live — merely verbose at a terminal, but the webui
  Agent Logs panel shells out to exactly this command over SSE, and a few thousand
  back-to-back lines (one DOM reflow each) froze the page with the `no session
  stream yet…` placeholder stuck on screen. `-f` now has `tail -f` semantics: it
  starts from the last N **raw stream records** (not rendered lines) and then
  follows. A new **`--tail N`** flag tunes the window — `50` is the default, `0`
  shows no backlog (only lines appended after the follow starts), `-1` restores
  the full-history replay. The window is follow-only (plain `ctl logs` still cats
  the full history and ignores `--tail`), applies to the `stream.jsonl` backlog
  only (sparse `ctl.jsonl` health lines are kept in full), and is identical for
  the rendered and `--json` paths — the raw path seeks to a record boundary so its
  byte-for-byte contract holds. A new `tailOffset()` helper in `cmd/ctl.go` finds
  the start of the last N complete records and `lineReader` applies it once at
  open. The panel hang is cured without any web change, since `handleAgentLogs`
  passes no `--tail` and so inherits the default.
- **Pin the 1-hour prompt-cache TTL for every spawned session; record the adapter's
  auth mode (drvctl-035).** The daemon-level `BaseSpec.Env` built in `newReconciler`
  (`cmd/ctl.go`) now carries `ENABLE_PROMPT_CACHING_1H=1`, which flows unchanged
  through `reconcile` → `manage` → the Claude Code adapter onto both spawn and
  resume, so the shared per-repo prefix stays warm across gaps longer than the
  default TTL (between tickets on a repo, a session parked on Needs-me). The adapter
  was confirmed to authenticate via a Claude.ai subscription (OAuth, no
  `ANTHROPIC_API_KEY`), where the 1h TTL is automatic but silently drops to 5m in
  usage-credit overage — so the drop-in is a real opt-in, not a no-op. The adapter
  itself stays auth-agnostic. `docs/prompt-caching.md` gains a "Rung B in practice"
  section with the per-auth-mode TTL table and where the pin is wired, and its open
  "adapter auth mode" question is marked resolved.
- **The invariant draiver protocol moves above the cache wall into the
  system-prompt append; the brief slims to spec + log (drvctl-034).** The protocol
  text is identical for every ticket, but it used to travel in the conversation
  layer, so every cold-start paid a fresh cache write for it. It now ships in the
  coding agent's `--append-system-prompt`, which can be cached once per repo
  (rung E in `docs/prompt-caching.md`). A new `internal/handbook` package embeds
  the canonical, byte-invariant `handbook.md` (the onboarding `SKILL.md` body
  verbatim) via `go:embed`, with `Content()` / `Version()` / `VersionOf()`
  accessors. A drift test keeps it pinned to `SKILL.md`, and a byte-invariance
  test stops per-ticket data from leaking into it. `resolvePromptCache`
  (`cmd/ctl.go`) now uses `handbook.Content()` as the **default**
  `SystemPromptAppend`. `append_system_prompt_file` in config overrides it, and an
  empty override file turns it off. `exclude_dynamic_system_prompt_sections` stays
  off by default and is still the opt-in lever for cross-ticket caching. The brief
  drops its `workingInstructions` nudge, which the append now carries, so the
  brief contains only the per-ticket spec and log replay. For A/B comparison and
  reproducibility, the protocol version is recorded as
  `session.Identity.ProtocolVersion` at spawn and folded into
  `attempt.Meta.ProtocolVersion` (`attempt.md`) when the attempt retires.
- **Web UI archive now goes through the `draiver archive` CLI verb (drvweb-020).**
  `handleArchive` no longer appends the archive event itself; it shells the new
  `runDraiverArchive` helper (`draiver archive --accepted|--abandoned --data
  --actor --attempt -- <id>`, from drvctl-043), restoring the `draiver` binary as
  the single write gateway for every board mutation. The written event is
  byte-identical to drvweb-019's, and the configured web actor is honoured.
- **The webui calls an in-process data-access gateway instead of shelling out to the
  `draiver` binary (drv-008).** A pure refactor — no new verbs, event types, or
  on-disk format changes. New `internal/repo` (`repo.New(root, actor)`) owns every
  ctl/webui authoring write (log, done, resolve, enable/disable, archive/unarchive,
  supervision, `attempt.md` provenance, `spec.md` edges) plus board/attempt/log
  reads; `internal/land` owns `merge --remote` orchestration; `internal/streamlog`
  owns session-log tail/render (`handleAgentLogs` now streams in-process over SSE).
  `cmd/` verbs and `internal/web` handlers are thin wrappers over the same
  functions, so archive has exactly one implementation; the `runDraiver*` helpers
  and `draiverBinOverride` are gone and web tests no longer build a binary. Daemon
  subsystems (`reconcile`, `gate`, `watch`, `limit`, `completion`) keep their own
  appends.
- **Acceptance criteria live as plain text in `spec.md`; the agent authors the
  Gherkin (drv-015).** `draiver new` now scaffolds an `## Acceptance criteria`
  section into every fresh `spec.md` — a light convention, no new schema. The
  handbook and onboarding `SKILL.md` gain a new §2 telling the attempt's agent to
  translate that prose into Gherkin `.feature` files and steps in the code repo,
  which run under the BDD rung and graduate into trunk on merge; the flow and its
  accepted flakiness trade-off are written up in `docs/acceptance-criteria.md`.
- **Attempt page top section folded into one tabbed card; Agent Logs becomes a
  bordered collapsible card (drv-021).** The stack of full-width panels at the top
  of the attempt detail page is now a single bordered tabbed card — **Spec**
  (default), **Provenance settings**, **Git controls**, **BDD report**, **Cache
  details** — so only the active panel takes vertical space. Conditional tabs
  follow their panel's existing gate (Git: `base`+`repo` set and not Done; BDD: has
  runs), so the card never shows an empty tab. Tabs switch client-side with a small
  vanilla script and full accessible semantics (`role=tablist/tab/tabpanel`,
  `aria-selected`/`aria-controls`, roving tabindex, arrow/Home/End keys). **Agent
  Logs** becomes a bordered card below it whose whole header row toggles
  expand/collapse; the Log section stays last. Layout only — no route, handler,
  data, htmx or SSE change, and every existing `data-testid` is preserved. A
  follow-up fix makes the BDD report load reliably: the iframe now ships with
  `data-src` and is promoted to `src` the first time its tab is revealed (the
  pattern Agent Logs uses for its SSE), since a `loading="lazy"` iframe inside a
  hidden tabpanel did not reliably load across browsers. Covered by
  `e2e/tests/attempt-tabs.spec.ts` and `features/attempt_tabs.feature`.
- **draiver's own BDD features reorganised into capability folders with tags and
  `Rule:` blocks (drv-024).** The flat `features/*.feature` files move to
  `features/attempt-page/` (tabs, git-controls, review-env) and
  `features/bdd-report/` (download, rerun); ticket ids leave the `#` banners and
  become feature-level tags (`@ui`, `@git`, `@review-env`, `@report`, `@drv-NNN`),
  and the provenance note becomes the `Feature:` description. The acceptance runner
  now discovers features recursively, parses tags, `Rule:`, rule-scoped
  `Background` (inlined into each scenario, so execution is unchanged) and
  descriptions, and emits them into `cucumber.json` (with an additive `Rule` field
  on `internal/cucumber.Element`), making draiver's own report the first real demo
  of drv-023's folder, tag and Rule navigation. Step behaviour is unchanged.
- **Agent handbook guardrails against working or pushing on the base branch
  (drv-033).** After an attempt `cd`'d to the brief's `Repo:` source tree (checked
  out on `main`) and pushed its fix straight to the remote, bypassing review,
  `internal/handbook/handbook.md` and the onboarding `SKILL.md` gain a **"Where you
  work"** section: the session already runs in its attempt worktree, so don't `cd`
  to an absolute repo path, and `Repo:` is only the tree the worktree was cut from.
  §9 now gates the push: `git branch --show-current` must print
  `draiver/<TICKET>/<attempt>`, and on the base branch the agent stops and
  escalates instead. Words only — no code enforcement; `handbook.Version()`'s
  digest changes accordingly.
- **Operator-action output is relayed into the System Logs stream (drv-048).**
  Promotion checks, git controls, and review-env up/down used to discard or truncate
  their subprocess output, so the webui System Logs panel showed only a one-line
  summary and never the diagnostics explaining a failure. Each family now relays its
  output into `system.jsonl`, one timestamped `SystemEvent` per line, interleaved
  with the existing started/verdict summaries: `handlePromote` streams
  `promote.Run`'s stdout/stderr through a line-buffered `systemRelay` writer
  (`ClassPromotion`, stderr in the error role) instead of `io.Discard`;
  `handleGitVerb` relays the report body after the summary line (`ClassGit`, error
  role on a failed verb); and a new nil-safe `reviewenv.Options.Relay` carries
  `Launch`'s up+confirm-up and `Teardown`'s down+confirm-down output. `Probe` and the
  daemon reaper leave the relay off, so polling never floods the panel. The durable
  ticket log is untouched — relay writes only to the rebuildable `system.jsonl`
  projection. BDD coverage in `features/system-logs/operator-output-relay.feature`.
- **CHANGELOG.md backfilled for every merged ticket, with a BDD guard on the
  invariant (drv-051).** The changelog had gone unmaintained: ~75 tickets merged to
  `main` had no entry. Every ticket id that prefixes a commit subject now has
  exactly one entry, written from its spec, attempt log, and merge diff, under the
  matching Keep-a-Changelog heading in `[Unreleased]`. The house shape is now a
  checked contract — one top-level bullet per ticket whose bold summary ends with
  the ticket id in parentheses and names no other id — because drv-052 splits the
  changelog into per-ticket fragments on it. A `@drv-051` scenario
  (`features/changelog/changelog-coverage.feature`) runs on the acceptance rung and
  compares `git log --format=%s` ticket prefixes against CHANGELOG entry ids, failing
  on any missing or duplicated entry, so a future ticket that lands without an entry
  turns the rung red.

### Fixed

- **Escalation resolve textarea no longer loses focus every 3s (drvweb-026).**
  Typing into an open escalation's resolve box (`#resolve-input-{seq}`) on the
  attempt page was interrupted on every 3s poll — the browser pulled focus out
  mid-keystroke. Root cause: the resolve textarea was the one polled input that
  *also* carried `hx-preserve="true"`. htmx runs its own `hx-preserve`
  node-retention on **every** swap, *before* handing off to the morph extension —
  it moved the live focused textarea out of the log region (parking it in a pantry
  after `</body>`, or, on engines without `Element.moveBefore()`, detaching it with
  `replaceChild`, which blurs it outright) and then moved it back, colliding with
  the fresh empty server textarea of the same id. The fix removes `hx-preserve` and
  lets idiomorph alone keep the field: its `restoreFocus` holds focus + caret, and a
  global `Idiomorph.defaults.ignoreActiveValue = true` (set after the extension
  loads, on both the attempt and board polling pages) stops the empty server value
  from clobbering the half-typed answer. A **dynamic** Playwright regression test
  (`e2e/tests/detail.spec.ts`) now types into the resolve box, waits out a real
  `/live` poll, and asserts focus, caret, and value all survive — closing the gap
  the static-only drvweb-025 coverage left.
- **Resolve textarea keeps focus and caret across the 3s log poll (drvweb-014).**
  On the attempt page the inline escalation **resolve** `<textarea>` lives inside
  the `log-region`, which htmx re-fetches and swaps (`innerHTML`) every 3 seconds.
  `hx-preserve` kept the typed *value*, but htmx relocated the preserved node
  during the swap, blurring it and dropping the caret — so the cursor jumped out
  of the box every ~3s and multi-word answers were nearly impossible to type. A
  small inline handler in `internal/web/templates/ticket.html` now snapshots the
  focused `.resolve-input` (id + selection) on `htmx:beforeSwap` and, on
  `htmx:afterSwap`, refocuses it (`preventScroll`) and restores the caret via
  `setSelectionRange` — synchronously, before paint, so there is no flicker. The
  log region still updates on its cadence, value retention is unchanged, and the
  input still vanishes once the escalation is resolved; the compose textarea sits
  outside the polled region and was never affected. Locked by
  `TestResolveInputKeepsFocusAcrossPoll`. (The approach was later reworked onto
  idiomorph in drvweb-026 after the morph swap reintroduced the bug.)
- **Enabling a `wants:`-coordinator holds it Pending instead of admitting it
  (drvctl-044).** When you enabled a Capability coordinator (a ticket with
  `wants:` edges), it was admitted as a live agent straight away and started
  working the whole epic. The design says it should enable its children and wait
  as a dormant Pending shell. `desired()`'s direct-enable pass admitted every
  enabled Running attempt unconditionally. The forward gate only trims attempts
  with unmet `after:`/`requires:` predecessors, and reverse-wants only wakes a
  coordinator that is already Pending, so neither one ever parked a coordinator.
  A new **coordinator dormancy gate** (`applyCoordinatorGate`,
  `internal/reconcile/coordinator_gate.go`) runs after `propagateWants` (so the
  children are still pulled into the fleet) and before `activateReverseWants`. It
  removes any desired attempt whose ticket has `wants:` edges from admission
  unless **all** of its wanted children are Done, which is when the coordinator is
  admitted for its own final review claim. Coordinators that are enabled through a
  parent are held the same way. Unlike the forward gate, this gate is **not
  fire-once**. A coordinator that the old code had already admitted is retired
  back to Pending on the next reconcile with no manual disable/re-enable, and a
  woken pre-digest coordinator parks again once its child's escalation resolves.
  Reverse-wants is unchanged: a child escalation still wakes a pre-digest
  coordinator, while a passthrough coordinator stays dormant until all of its
  children are Done.
- **Sessions no longer die at spawn on the stored short model id — `opus-4.8` is
  resolved to `claude-opus-4-8` before `claude --model` (drvctl-045).** draiver
  stores models in a short form (`opus-4.8`, as the `--model` flag help and the
  `attempt.md` writer document), but the Claude Code adapter passed it verbatim to
  `claude --model`, which rejects it ("There's an issue with the selected model"),
  so every daemon-spawned session failed before starting. `baseArgs` in
  `internal/agent/claudecode` now routes the model through a new `resolveModel`
  helper backed by a small `modelAliases` map (`opus-4.8` → `claude-opus-4-8`),
  kept next to `DefaultBin` at the Claude-specific seam. Unmapped values — full
  `claude-…` ids and the CLI's bare aliases (`opus`, `sonnet`, `haiku`) — pass
  through unchanged, and an empty model still emits no `--model` flag. Spawn and
  Resume both go through `baseArgs`, so a model from either `session.json` or
  `attempt.md` is covered; the earlier one-off data rewrite of existing attempts is
  no longer needed for new ones. `TestBaseArgsModelResolution` covers the map hit,
  pass-through and empty cases.
- **A drifted or clashing worktree self-heals or escalates instead of wedging
  admit forever (drvctl-046).** `internal/worktree` identified a managed worktree
  two ways — by **branch** (`List`/`find`/`Reconcile`) and by **path**
  (`Create`/`Remove`/`Dirty`/`pathFor`) — and nothing enforced that they agreed,
  so a mismatch failed the same way every reconcile tick as a `worktree-clash`
  with no recovery. Four fixes, sharing one set of helpers (`branchAt`,
  `listRecords`, `worktreeOnBranch`, `keyForPath`, `removeAtPath`): **(B) a
  managed path drifted onto another branch or a detached HEAD** is now reclaimed
  by `Create` (`worktree remove --force` + `prune`, then re-added on the attempt's
  branch) — force-reclaim was chosen over refuse-if-dirty because the path is
  draiver's by construction; **(A) the attempt's branch checked out at a foreign
  path** (typically the origin repo) makes `Create` return an `ErrCreate` naming
  the offending worktree, still on the transient `worktree-clash` health path —
  draiver never steals a human's checkout; **`Reconcile` no longer aborts** on a
  zero-key (foreign/detached) entry under the managed base — it sweeps such strays
  by path and returns them, and the reconciler's `Adopt` raises one idempotent
  escalation per stray, flipping the attempt to Needs-me (mirroring
  `escalateNoRepo`; the escalation drvctl-027 deferred); and **`Dirty` is
  branch-aware** — a path not on the attempt's branch reports dirty, so the
  retire guard preserves it rather than force-removing a checkout whose real state
  it never read.
- **The board's play button no longer appears on an attempt already running
  through a parent edge (drvweb-022).** The green enable button
  (`POST /ticket/{id}/{attempt}/enable`) showed up on cards whose agent was
  already live because a parent's `wants:`/`after:` edge had activated it. Such a
  child never gets a direct `enable` event, but `canEnable` only checked the
  durable log bits (`Running && !Enabled`). Clicking the button was not a no-op
  either. `handleEnable` loaded the attempt without its runtime bits and wrote a
  real `enable` event, which turned an enabled-via-parent child into a directly
  enabled attempt whose supervision no longer followed its parent. `canEnable` now
  also requires `!Live && !Desired`, so the button follows the effective (Control)
  state. `handleEnable` re-derives `Live`/`Desired` with a new `fillRuntime`
  helper (the same `DeriveDesired` + `attemptLive` logic as `board()`) before
  applying the same check, so a POST for a card that shows no button writes
  nothing.
- **BDD report rerun now runs against the attempt's reviewed commit, and tag facets
  sort alphabetically (drv-030).** A board rerun previously executed the bound rung
  in the shared main checkout — whatever branch it happened to be on — and stamped
  the run with that tree's HEAD or `nocommit`, so a rerun could silently report on a
  different codebase (on drvweb-023 a branch-only feature vanished from the rerun
  report). `bddexec.Rerun` now resolves the reviewed commit (live coding-worktree
  HEAD, else the attempt branch tip — the rule review environments use), cuts a
  throwaway detached worktree at it, runs up/healthcheck/rung/capture there, tears
  it down, and stamps the capture with that real commit; the `nocommit` fallback is
  gone. The environment contract and non-terminal behaviour are unchanged, as is
  the call site. Separately, `facetList` orders the tag facet bar by tag name
  rather than scenario count, so it no longer reshuffles between runs. New scenarios
  in `features/bdd-report/rerun.feature` and `facets.feature`; the rerun test
  harness is now git-backed. The rerun-button gate (`attemptHasBDDRung`) still reads
  the main checkout's pyramid — left as a follow-up.
- **Resume actually resumes: the turn is primed before `confirmOnline`
  (drvctl-056).** The resume fast-path never worked against headless `claude`. A
  bring-up with a recorded session id (every wake after `park`, every restart,
  every re-admit from Pending) ran `claude --resume` and then waited up to
  `ResumeConfirm` for a `system/init` frame *before* sending any prompt. Headless
  `--resume` emits nothing until it receives a user turn, so the confirm always
  timed out, the process was killed, and the attempt silently cold-started with a
  new session id. Now `bringUp` (`internal/reconcile/bringup.go`) injects the
  resume nudge **before** `confirmOnline`, and `drive()` sends the brief only to a
  fresh spawn. An id that can't be resumed still times out and falls through to
  `Spawn` plus the brief, and the primed turn dies with the killed process, so the
  agent is never prompted twice. Resume also requires **context headroom** now: a
  session whose `meter.json` context fill is at or above 80% of `ContextLimit`
  cold-starts with a clean window instead of resuming straight into an auto-stop.
  A missing meter or an unset limit counts as headroom. The resume nudge is
  **wake-aware**: `protocol.InjectResumeNudge` takes a wake note. On an async
  `test --detach && park` wake, the note names the `test-result` that landed, so
  the agent can read the verdict without a `recall` round-trip. The reconcile fake
  adapter now emits `init` only after the first input on a resume, matching real
  `claude`. Its old unconditional `init` is what hid this bug. A new acceptance
  feature (`features/manager-as-worker/resume-on-wake.feature`) covers the
  delivered turn.
- **Markdown pipe tables render as styled HTML tables in the webui (drvweb-024).**
  The webui's single shared goldmark instance (`internal/web/web.go`) was built
  without the table extension, so GFM pipe tables in event bodies, card reasons and
  `spec.md` rendered as literal `|`/`---` text. It now registers
  `extension.Table` — deliberately not the full `extension.GFM`, whose autolinks
  would widen the destination set `linkPolicy` must guard — alongside the existing
  parser options, so the http/https/relative-only link scheme floor is unchanged
  (a `javascript:` link inside a cell is still blanked). The extension ships inside
  the already-required goldmark v1.8.5, so there is no new module dependency.
  Rendered tables get a minimal `.prose` grid in `style.css` — hairline cell
  borders, a quiet header row and zebra striping, all keyed to the
  `--line`/`--panel-2`/`--ink` theme tokens so they read in light and dark. A BDD
  scenario (`features/attempt-page/markdown-tables.feature`) screenshots a rendered
  table on the ticket detail page.
- **Board and live log keep your text selection across the 3s poll
  (drvweb-025).** The board (`#board-region`) and the attempt live region both
  polled every 3s with `hx-swap="innerHTML"`, which replaced the whole subtree on
  every tick and wiped any text selection, so you could not copy from the board or
  the live log. Both regions now swap with `hx-ext="morph"` /
  `hx-swap="morph:innerHTML"` using a vendored idiomorph 0.8.0 extension
  (`internal/web/static/idiomorph-ext.min.js`, served from the embedded static
  FS, no CDN). Morph diffs the incoming fragment against the live DOM: a poll with
  no changes makes zero mutations, so selection, caret and `:target` survive, and a
  changed cell updates in place without disturbing a selection elsewhere. ETag/304
  was rejected because htmx still swaps a revalidated body. Two workarounds from
  the `innerHTML` era were removed: the resolve-textarea focus/caret snapshot
  script and the `.is-target` mirror (native `:target` now persists, and the CSS
  highlight is just `.event:target`). Out-of-band badge/count updates and the 286
  poll self-cancel still work. Covered by
  `features/polling-morph/selection.feature` and Playwright specs that select text,
  wait out a real poll, and assert the selection survives. (The textarea's
  remaining `hx-preserve` was removed later by drvweb-026.)
- **Browser Back no longer restores a stale board (drv-042).** The full board page
  (`GET /`, `handleBoardPage`) now sends `Cache-Control: no-store`, so Back
  re-requests the page and renders fresh state instead of restoring an old
  back/forward-cache DOM snapshot. The header is scoped to the full-page handler —
  the `/board` partial and the 3s morph poll are unchanged. Acceptance scenarios
  with screenshots in `features/board-cache/no-store.feature`.
- **Board promotion-check spinner no longer disappears while the check is still
  running (drv-049).** The spinner on a promotion chip (drv-046) relied on htmx's
  temporary `.htmx-request` class. Any `qa` run longer than the board's 3s poll
  lost its spinner when the poll re-rendered the card, so the check looked like it
  had silently failed. "Check in progress" is now recorded on the server.
  `handlePromote` appends a new `promotion-check` event (`started`, at HEAD) to
  the attempt log before `promote.Run`. A pass/fail `promotion-result` closes it;
  a blocked or environment-fault run closes it with an explicit `cleared` event
  and leaves the earlier verdict intact. `DerivePromotions` folds the marker into
  `PromotionStatus.InProgress`/`StartedAt`, and the chip renders a server-side
  `promo-run` state that every poll redraws. The `.htmx-request` path stays for
  instant click feedback. A marker left behind when a check dies mid-run expires
  after 15 minutes (`promoCheckStaleAfter`), so a chip never spins forever or turns
  falsely green, and mark-done is withheld while a check is in flight. For
  consistency, the attempt page's Promotions **Check** button loses its confirm
  dialog and gains the same inline spinner. Covered by
  `features/promotions/spinner-durable.feature` plus unit tests for the event
  folding, chip states and marker expiry.
- **A resumed agent is told what was logged while it was parked, so a
  decision-reopen is no longer silently ignored (drvctl-058).** When a human adds a
  `decision` to a Review attempt (or resolves an escalation), the attempt flips
  back to Running and the reconciler resumes the kept-warm session — but that
  session's restored context ends at its own hand-off, and the plain continuation
  nudge gave no hint the log had changed, so the agent re-claimed review with stale
  context without ever reading the decision. This regressed in drvctl-056: before
  resume worked, every bring-up fell through to a cold-start brief whose "Current
  understanding" index surfaced the decision by accident. `asyncWakeNote` in
  `internal/reconcile/bringup.go` (which only handled detached test-result wakes)
  is generalised into **`resumeWakeNote`**: it watermarks on the attempt's last
  **session-authored** event (`agent:` actor, excluding `test-result` and
  `woken-by`, which carry an agent actor but are external input), then names every
  authoritative event above it — `decision`, `resolution`, `note`, `gotcha`,
  `test-result` — by seq and headline, and tells the agent to `draiver recall` (or
  `brief`) and act on them before handing off. The detached-test wake is now the
  single-`test-result` case of the same note, keeping drvctl-056's wording (read
  the verdict, hand off, no recall round-trip); a pure continuation resume with
  nothing new still gets the plain nudge. Covered by
  `TestDecisionReopenResumeNamesTheDecision` and a new decision-reopen scenario in
  `features/manager-as-worker/resume-on-wake.feature`.

## [0.4.0] - 2026-08-10

### Added

- **Set an attempt's repo/base from the board — the provenance table
  (drvweb-009).** A Review attempt wedged for want of a `base:`/`repo:` (the two
  fields `ctl merge`/`sync` gate on) could only be unblocked by hand-editing
  `attempt.md` or recreating the attempt — losing its log and branch. A quiet
  key/value **provenance table** now sits above the Spec on the attempt detail
  page, showing the attempt's `repo`/`base`; a Review attempt that records no
  base gets a merge-blocking callout. Each value reads as plain text until
  clicked, when it becomes an inline input — the enclosing form posts on any
  child's `change` (a blur that altered the value) or on Enter, so clicking out
  saves and there is **no Save button**; an (i) bubble per key carries a
  hover/focus tooltip explaining what that field gates. The write is not
  reimplemented in the web layer: `POST /ticket/{id}/{attempt}/provenance` shells
  **`draiver attempt set`** (drvctl-028), which owns the `attempt.md` write and
  the field validation, keeping the package's write invariant that the web layer
  never touches `attempt.md`. Only `repo`/`base` are read from the form (the
  `tool`/`model` fields the verb also accepts are deliberately out of the UI's
  scope, the same allow-list discipline the compose box uses); the panel prefills
  current values, so a blank field means *leave unchanged* — it is omitted from
  the shell-out, and an all-blank submit is a 400 rather than a 500 from the
  verb's "nothing to set". No `--actor`: `attempt set` appends no hash-chained
  event. On success the panel re-renders in place (htmx swap) with the saved
  values and a confirmation, behind the same-origin and 404 guards as the other
  writes.
- **"Merged elsewhere — close & pull" review action (drvweb-010).** A third
  review action joins Decision and Done on the attempt page —
  **"Merged elsewhere — close & pull `<base>`"** — the board affordance for a PR
  landed on the forge. It is shown only for a Review attempt that records **both**
  a base and a repo, and `POST /ticket/{id}/{attempt}/merge-remote` shells
  **`draiver ctl merge --remote`** (drvctl-029): the CLI owns the `git fetch` +
  containment proof + `done` and the best-effort local fast-forward, so the web
  layer never touches git, per the package write invariant. Unlike the other web
  writes, the handler returns the verb's **combined stdout** on success — the
  best-effort pull's result or warning is printed there, not recorded in the
  `done` event — and pairs the attempt-live fragment (the log region flips to
  Done, OOB badge/count) with an OOB result banner carrying that message. A
  zero-exit-with-warning (e.g. a dirty or diverged local base that skipped the
  pull) is treated as success and displayed; a nonzero exit (a failed containment
  or fetch) surfaces as a 500 like the other writes.
- **Self-documenting attempt.md/spec.md frontmatter and `draiver attempt set`
  to edit provenance after creation (drvctl-028).** The provenance fields that
  gate the whole land flow (`repo:`/`base:`, which `ctl merge`/`sync` demand)
  used to vanish from `attempt.md` the moment they were unset — `omitempty` plus
  a whole-struct `yaml.Marshal` dropped the key entirely, so a legacy or
  repo-unresolved attempt gave a hand-editor *no hint the fields even exist*. And
  there was no verb to set them after `attempt new`: fixing a Review attempt
  wedged for want of a `base:` meant recreating it (losing its log/branch) or
  hand-editing YAML. Now `writeMeta` hand-renders the block: a set field renders
  as a real line, an **unset** optional field (`repo`/`base`/`tool`/`model`)
  renders as a commented example naming the key, a sample value, and what
  consumes it (e.g. `# base: main  # branch this attempt lands into; used by
  ctl merge/sync`). `parseMeta` ignores YAML comments, so a write→parse→write
  cycle is idempotent. `scaffoldSpec` gets the same treatment for spec.md's
  optional `project`/`team`/`assignee` board-grouping metadata. The new
  **`draiver attempt set <ticket[@attempt]>`** verb (flags `--repo`, `--base`,
  `--tool`, `--model`) writes those fields into an existing `attempt.md` — the
  exact analogue of `draiver title` on spec.md: static metadata, so it appends
  **no** log event and never touches the audit chain, taking effect on the next
  board/merge read. It is a *targeted* setter — only an explicitly-passed flag
  overwrites its field, so `attempt set X --base main` never clobbers `repo` —
  reusing `attempt new`'s validation (`--repo` required-if-given; `--base`
  defaults to the repo's current branch via `resolveBase` when passed empty).
  This verb is the write path a future webui provenance form shells out to
  (drvweb-009).
- **Live "Agent Logs" stream on the attempt page, plus a capped "more" Spec
  (drvweb-011).** The attempt detail page showed only the Spec and the ticketlog
  Log; to watch what the agent was actually doing — tool by tool — a supervisor
  had to drop to a shell and run `ctl logs -f`. A new collapsible **Agent Logs**
  section sits between Spec and Log: collapsed by default (`<details>`), on expand
  it opens a Server-Sent-Events stream (`GET /ticket/{id}/{attempt}/agent-logs`,
  the web layer's first stream — everything else is htmx polling) that relays the
  same content `ctl logs -f` prints, the agent stream interleaved with the
  drvctl-027 `session/ctl.jsonl` health transitions. The handler **shells out to
  `draiver ctl logs -f`** (the same write-path-reuse discipline as the log/resolve/
  enable POSTs) and relays each stdout line as one SSE `data:` event, bound to the
  request context via `exec.CommandContext` so collapsing the panel or leaving the
  page kills the follow — one connection per expanded panel, none for a collapsed
  one. Over a pipe the CLI renders **plain, uncoloured** text (`newStreamRenderer`
  gates styling on `term.IsTerminal`), which is exactly what this ticket wants;
  ANSI/role colouring is deferred. Lines are rendered into a monospace `<pre>` via
  `textContent`, never the markdown→HTML path, so the untrusted session content
  (arbitrary tool output + model prose) cannot inject markup. A hand-run attempt
  with no session yet shows a quiet placeholder, not an error. Separately, a long
  Spec no longer pushes the Log off-screen: the Spec is capped to a max-height with
  a fade and a **more/less** toggle (pure CSS + a small JS toggle, no server change
  — `SpecHTML` is already rendered and sanitised), and the cap is dropped entirely
  when the spec is short enough not to need one.
- **`ctl merge --remote[=NAME]` — close a ticket landed by an external PR
  (drvctl-029).** The increasingly common land path is not `ctl merge` but a PR
  merged on the forge; afterwards the code is in the *remote* base and draiver had
  no verb for it, leaving the user to hand-run `git fetch` + a fast-forward and then
  `done`. `--remote` is the external twin of `ctl merge`: instead of landing the
  local branch it (1) `git fetch`es the attempt's recorded `base:` from the remote —
  the **only** place draiver reaches a remote, and it **never pushes**; (2) verifies
  the branch tip is contained in the fetched `refs/remotes/<remote>/<base>`, the
  proof the PR really merged (if not, it refuses and records nothing, taking the
  same `--escalate`/`--no-escalate` disposition as the other land verbs); (3)
  records `done` so control state follows reality — the code is in the base, just
  the remote base this time; and (4) **best-effort** fast-forwards the local base to
  the fetched ref. Step 4 is hygiene, never load-bearing: a missing, dirty, or
  diverged local base is a printed **warning**, never un-closing the ticket the
  containment gate already proved landed. It is gated to a stopped `Review` attempt
  (still the Review → Done transition) but, unlike local `ctl merge`, does **not**
  require a clean base checkout — a dirty base just skips the pull. `NAME` defaults
  like `review`'s rule: exactly one remote → use it; several remotes → the config's
  `primary_remote`, else refuse asking which. draiver still never deletes the branch
  or talks to the forge API. This is the verb the webui's "Merged elsewhere — close
  & pull" button (drvweb-010) shells out to.
- **Red error dot on the board when draiverctld can't run an attempt
  (drvweb-008).** A `Running` + enabled attempt the daemon cannot bring up used
  to render as healthy/idle — worst of all, a `restart --new-session` wedge sat at
  `session_id=""`, the exact case the session-liveness dot drew as *nothing*. The
  dot now also reads the per-attempt health log from drvctl-027
  (`session/ctl.jsonl`): it lights a distinct crimson
  (`Waratah`, a hue apart from the rust reserved for Stuck) with a
  `title`/aria-label when any daemon error class is currently open in
  `session/ctl.jsonl`. The rule is exactly the log's edge-triggered shape —
  most-recent-transition-per-class-wins, so a self-healed blip that already logged
  its `error-end` shows nothing — and it is **class-agnostic**: any open class
  lights the dot (a `worktree-clash`, or the `admit-failed` catch-all that covers
  the corrupt-object variant), so no real wedge renders as nothing. The error dot
  is checked before the empty-`session_id`/no-`session.json` early-returns so the
  founding wedge is visible, and outranks stopped/disabled/none; a live pid still
  wins (a running process and a run-failure are mutually exclusive). The web
  server stays strictly read-only and out-of-process — it `os.ReadFile`s
  `ctl.jsonl` itself, exactly as it already reads `session.json` and probes pids,
  never talking to the daemon.
- **Per-attempt daemon health log at `session/ctl.jsonl`, surfaced in `ctl logs`
  (drvctl-027).** When the supervisor cannot run an enabled attempt the failure
  used to be invisible — `admit` failed every tick with the operational `Logf`
  defaulting to a no-op and `ctl logs` showing only the agent stream, so a wedged
  attempt (its branch checked out in another worktree, or a corrupt/empty object
  at the branch tip) looked idle. Each attempt now gets an append-only,
  rebuildable `session/ctl.jsonl` (new `Root.SessionCtlLogPath`) recording
  **edge-triggered** daemon health transitions — an `error-start` when an error
  class begins affecting the attempt and an `error-end` when it clears, not one
  line per failed tick. Edges are diffed against an in-memory per-attempt
  active-class set beside the run table (rebuildable — a restart re-observes and
  re-emits), and an end-of-tick sweep closes classes for attempts that leave the
  desired set so the board's red dot resolves instead of stranding on a lone
  start. The worktree-clash class is recognized by cause — a new
  `worktree.ErrCreate` sentinel wrapping every `git worktree add` failure — rather
  than by string-matching git stderr, so it spans *any* cut failure (branch
  checked out elsewhere, corrupt object at the tip, missing base ref, occupied
  path); everything else falls to an `admit-failed` catch-all. Class strings are a
  stable contract with the board's red error dot (drvweb-008); `no-network` /
  `model-unreachable` stay reserved for a future stream-health seam. `ctl logs`
  interleaves the health lines with the agent stream in the human render (error
  role for `start`, system role for `end`, each with its own recorded timestamp)
  and `-f` follows both files; the daemon's free-text `Logf` now routes to stderr
  (was a no-op) and `--json` passthrough of `stream.jsonl` is unchanged.

### Changed

- **Readable `ctl logs` — a fixed-width `[time · tokens · role]:` prefix and
  Markdown-rendered prose (drvctl-025).** The shared session renderer marked each
  line with ad-hoc glyphs, and the assistant's prose printed as raw Markdown
  source. Every line now carries a fixed-width, greppable
  **`[<datetime>  <tokens>  <role>]: <content>`** prefix, and assistant prose is
  rendered from Markdown to styled ANSI via `charmbracelet/glamour` (over the
  goldmark parser already vendored). `renderEvent` moves onto a stateful
  `streamRenderer` so the tokens column carries the **last-known cumulative
  context-window snapshot** forward across the many events that report no usage —
  a cost-only `result` frame (ctx=0) no longer clobbers it. Roles map to a small
  vocabulary (assistant/tool/system/perm/error), each tinted on a TTY — **only
  the role token is coloured**, the datetime, token tally, and brackets stay
  plain, with the role column hand-padded so the closing bracket still aligns past
  the colour escapes — and tool output is italicised; usage-only frames print
  nothing (their figures ride the next line's prefix). The timestamp is
  render-time wallclock (the stream carries none), and `--json` / `stream.jsonl`
  stay byte-for-byte unchanged. Non-TTY output uses glamour's notty style so no
  ANSI leaks into a pipe or `jq`; a bespoke ANSI-aware trim strips glamour's
  per-cell colour padding on a TTY. One renderer is still shared by the live
  follow and disk replay.

## [0.3.0] - 2026-08-07

### Added

- **Landing is now a `ctl` primitive — `ctl merge` / `ctl sync` on a per-attempt
  base branch (drvctl-021).** Every attempt records a `base:` branch (default: the
  bound repo's current branch at create, overridable with `--base` on `draiver
  new` / `draiver attempt new`), and the worktree is cut from it so a fresh land is
  a clean fast-forward. `ctl merge <ticket>` lands the attempt's branch into its
  base **fast-forward only** — gated to a stopped Review attempt with a clean
  checkout, keeping the branch until the land is durable, and recording `done` on
  success so Done comes to mean *the code is in the target branch*. `ctl sync
  <ticket>` back-merges the base into the branch additively (one merge commit, no
  SHA rewrite — no rebase anywhere), so a resume continues on the updated tip and a
  diverged branch becomes ff-landable; a diverged `merge` refuses and points to
  `sync`, or `merge --sync` does sync-then-ff in one shot. `ctl merge --dry-run`
  reports mergeability (via `--is-ancestor` + `merge-tree`) without mutating
  anything. On failure the orthogonal `--escalate` / `--no-escalate` axis picks the
  disposition, defaulting by actor kind: an `agent:*` land raises a durable
  escalation (→ Needs me, halt), a `human:*` land exits nonzero with a message.
  Conflict / divergence / dirty always abort cleanly and never force.
- **Inline escalation resolution from the board (drvweb-007).** Each *open*
  escalation on the attempt timeline now renders a resolution textarea + Resolve
  button; submitting appends a `resolution` event refing that escalation's seq,
  clears the block, and leaves Stuck — the board affordance for `draiver resolve`.
  A resolved escalation shows only "→ resolved by #N". Like the other webui
  writes, it is not reimplemented in the web layer: `POST
  /ticket/{id}/{attempt}/resolve` shells `draiver resolve` (runDraiverResolve, via
  a shared `draiverExe()` helper), so the board and a terminal share one append
  path. Guards mirror the CLI and close the gap it leaves: same-origin CSRF (403),
  empty answer (400), and `classifyResolveTarget` rejects an unknown or
  non-escalation seq (400) and an already-resolved seq (409), all read from
  derived state rather than the posted form. The textarea carries a stable id +
  `hx-preserve` so the 3s poll swap never clobbers a half-typed answer and
  correctly vanishes once resolved. The webui is now a three-write surface (log,
  resolve, enable).
- **Agents are prompted to push and attach a review URL when raising review
  (drvctl-026).** The onboarding skill's "Claim review" step (SKILL.md §5) and the
  Loop line now teach the agent to push its attempt branch to a git remote and
  claim review with a link the human can click — a `compare` URL by default
  (`<base>/compare/<main>...<branch>`, derived from `git remote get-url` with a
  trailing `.git` stripped), or a real PR via `--url`. Remote selection is
  explicit: exactly one remote → use it; more than one → the new global-config key
  `primary_remote` (a git remote name, sitting alongside `review_link_hosts` in
  `~/.draiver/config.json`); ambiguous (several remotes with no primary, or a
  primary absent from `git remote`) → escalate rather than guess. `draiver review`
  is unchanged behaviourally — it still only appends and validates the link, never
  pushing or opening a PR — but its `--help` now says so. `config.Config` gains an
  optional `primary_remote` field (omitted → empty).
- **Green play button on the board's Running column, the webui's first write
  (drvweb-005).** A Running attempt that is not yet enabled (the supervision axis
  the grey session-dot reads) shows a green play button on its card; clicking it
  opts the attempt into daemon supervision and the card re-renders in place (the
  button disappears, the dot flips off grey). The write is not reimplemented in
  the web layer — `POST /ticket/{id}/{attempt}/enable` shells `draiver ctl enable`,
  the same verb a human runs at a terminal, so there is a single enable code path.
  Bare enable only: a running `ctl up` brings the attempt up on its next tick, and
  with no daemon the enable simply waits (no control socket in the web process).
  The write is attributable (`--actor` / `$DRAIVER_ACTOR`, else `human:webui`) and
  same-origin guarded; it is idempotent (an already-enabled attempt is a no-op).
- **Compose typed log entries from the webui, with Review → Decision / Done
  actions (drvweb-006).** The attempt detail page gains a compose box — a type
  `<select>` (`note`, `gotcha`, `decision`) plus a markdown body — that appends a
  typed event to the attempt without dropping to a terminal, and two Review-gated
  buttons: **Decision** (reopens a Review attempt to Running) and **Done** (closes
  it), both driven by the existing lifecycle `Derive`. This makes the board a
  write surface for the first time: `POST /ticket/{id}/{attempt}/log` performs
  the append by shelling the draiver CLI (`draiver log --type …`, and `draiver
  done` for the terminal action) rather than reimplementing it in the web layer,
  so the webui and a human at a terminal share one append path. It passes a
  resolved actor (`$DRAIVER_ACTOR`, else `human:$USER`, else `human:web`) through
  as `--actor` so a web-composed entry is attributable exactly like a CLI one. The
  type is checked against a curated allow-list `{note, gotcha, decision, done}`,
  so lifecycle types with their own flows (`escalation`/`resolution`/`review`/
  `enable`/`disable`) can never be hand-typed (400, nothing appended). The
  state-changing POST is guarded by an Origin/Referer same-host check (403 on
  cross-origin; header-less non-browser clients pass). One response re-renders the
  log region and OOB-swaps the state badge and log count, so a Decision/Done
  visibly flips the badge, and a Done then self-cancels the log poll on its next
  `/live` tick (286). The `web` package is now read-mostly rather than read-only.
- **Forge-neutral review links on the event log (`review --url`, drv-007).** An
  event can now carry one or more opaque review links — a draft PR, merge request,
  or diff URL — set by the agent at the moment it claims review, on the append-only,
  hash-chained log with full provenance. The schema gains `Links []Link`
  (`{Rel, Href}`, `yaml:"links,omitempty"`), included in the hashable projection so
  links are tamper-evident under `draiver audit`; `omitempty` keeps existing
  link-less events hashing identically, so there is no migration. `draiver review`
  and `draiver log` gain a repeatable `--url <uri>` (rel defaults to `pr`) and an
  explicit `--link <rel>=<uri>` for other rels (`mr`, `diff`, `ci`, …). `Rel` is an
  uninterpreted free label — draiver never parses the host or path and carries no
  forge-specific code. At append time `Href` must parse as an absolute URL whose
  scheme is in a **hardcoded `{http, https}` allowlist** (a safety floor,
  deliberately not operator-configurable — an editable scheme list reopens
  `javascript:`/`file:`/`data:`); a rejected link fails the command and writes
  nothing. `config.Config` gains an optional `review_link_hosts` allowlist
  alongside `Permissions` — empty (the default) admits any http/https host, so the
  host policy is opt-in and off by default. draiver never fetches the URL.
- **Review links surfaced as one-click actions on the board (drvweb-004).** A
  Review-column card whose latest `review` event carries a link now shows a
  distinct **"Review changes ↗"** action — the primary link (`rel` `pr`/`mr`, else
  the first) — opening the PR/diff in a new tab (`target="_blank"
  rel="noopener noreferrer"`), *additional to* and visually separate from the
  existing internal deep-link. No link → no button (a direct-merge flow degrades to
  nothing). The action is scoped to the Review column, so a reopened or blocked
  attempt never shows a stale PR button. On the attempt detail timeline, every
  event renders its links as chips labelled by `rel`, below the body. Links are
  rendered **safely**: the http/https scheme allowlist is **re-checked at render**
  (defense in depth, independent of the append-time check), and the board never
  fetches or previews the URL.
- **Sanitizer hardening for rendered markdown bodies (drvweb-004).** Audited the
  bare `goldmark.New()` renderer: its default already blanks `javascript:`,
  `vbscript:`, `file:`, and non-image `data:` link hrefs and omits raw HTML, but it
  admits `data:image/{png,gif,jpeg,webp}` links and autolinks. Added a goldmark AST
  transformer (`linkPolicy`) that enforces an http/https-or-relative scheme floor on
  every body link/image/autolink, closing that carve-out so a `data:`/`javascript:`
  link in an event body can never produce a live href.

### Changed

- **The injected brief now carries the Markdown-when-logging nudge (`draiverctl`,
  drvctl-020).** The board renders event bodies as Markdown, but that how-to-log
  guidance only reached agents that loaded the onboarding skill (§3.5) — sessions
  the supervisor drives get their instructions from the cold-start brief, which
  carried none, so a supervised agent without the skill logged unstructured blobs.
  `brief.Build` now prepends a one-line working-instructions preamble,
  single-sourced from a `workingInstructions` const kept in sync with SKILL §3.5.
  It rides every cold-start and resume (`protocol.InjectBrief` re-injects the
  brief after Spawn/Resume) and sits with the spec/log the agent already reads.
- **Session lifecycle verbs recast around a state-stack "degree axis", and the
  imperative verbs now hand off to the daemon (`draiverctl`, drvctl-016).**
  `start`/`stop`/`restart` were an ad-hoc foreground driver: the resume path blindly
  `--resume`'d any recorded session id — so an id that could no longer be resumed
  was retried forever with no exit but a manual `rm -rf …/session/` — while
  `restart` resumed the same id yet force-fed a fresh brief, neither a clean
  continue nor a clean fresh start. Bring-up is now one **self-heal cascade** that
  climbs from the highest surviving layer: a recorded session is Resumed and
  **confirmed online** (the first `system`/init frame, via a new adapter-agnostic
  `agent.Onliner` seam); an id that no longer resumes has its dead process reaped
  and falls through to a fresh `Spawn` on the same worktree. **Brief-on-reset** is
  coupled to that: a fresh spawn is cold-started from the brief, a resume continues
  without a re-brief — so the "restart re-briefs a resume" incoherence is gone.
- **`restart` is now flush-to-depth + that same climb, with the reset depth named
  explicitly (`draiverctl`, drvctl-016).** A single ordinal flush runs before the
  cascade climbs back, so all points on the axis share one path: `--new-session`
  (clear the session id → respawn fresh on the same worktree, cold-started),
  `--new-worktree` (also rebuild the checkout from HEAD, discarding uncommitted
  work by design), and `--new-attempt` (fork a child attempt via
  `attempt.Create{From}`, inheriting tool/model/repo, the parent log preserved
  immutably with a fork note). Deepest flag wins; every destructive reset records a
  durable note so it is never silent. The old "context refresh" framing is dropped.
- **`start`/`restart`/`enable --now` are imperative-transient control-plane actions
  that hand an attempt to a running `ctl up` (`draiverctl`, drvctl-016).** The
  self-heal cascade and brief-on-reset coupling now live only in the daemon-shared
  bring-up path, so the client verbs cannot drift from the fleet. `ctl up` claims a
  **controller pidfile** (`controller.json` — pid, boot nonce, started; removed on
  clean exit, liveness-probed so a stale record never passes for a running daemon)
  and prints its pid on startup. `start`/`restart` **require a live controller**
  (they error if none is up, and `restart` never reaps a session it cannot hand
  back), then stamp a per-attempt **desired-marker** with the daemon's boot nonce —
  transient by construction, swept when the daemon restarts, so they never leave an
  unsupervised orphan; the verb prints a handoff and returns (watch via `ctl logs
  -f`). `stop` reaps and removes the marker, leaving the imperative fleet, while a
  spent-but-still-desired run is re-admitted by the daemon. `start --new-attempt`
  forks a parallel branch and leaves the parent running; `restart --new-attempt`
  parks the parent so only the fork runs.

### Fixed

- **Resumed sessions get a driving turn again, and a session can no longer stop
  at `result: success` without handing off (`draiverctl`, drvctl-022).** drvctl-016's
  brief-on-reset coupling gated the sole driving prompt behind `if w.spawned`, so a
  resumed headless `stream-json` session — which produces nothing until it receives a
  user turn — came online, restored its context, and idled forever, never continuing
  the work and never filing a `review` or `escalation`. From the supervisor's stream
  the attempt "got to `result: success` and stopped" with nothing filed, stranding it
  in Running + enabled; this broke every resume path, including the core
  `escalate → resolve → resume` arc. **Part A** restores drive keyed on session
  identity: `admit` branches on `wired.spawned` — a fresh spawn (empty context) still
  gets the full cold-start brief, while a resume (recorded id came back online, context
  intact) gets a new short `protocol.InjectResumeNudge` that points to `draiver brief
  <ticket>` and restates the log/hand-off obligation without re-dumping spec + log.
  **Part B** adds defense in depth: a new `internal/completion.Gate`, constructed in
  `bringUp` and threaded through `ingest`/`dispatch`, reacts to `agent.EventTurnEnd`
  with `Turn == "success"`; using the same log-tail-vs-baseline check the protocol gate
  uses, it looks for a `review`/`escalation` newer than the session baseline, injects a
  one-shot nudge if the obligation is unmet, and escalates-and-halts to a human on a
  later still-unmet success turn — so a stalled "done" lands on the board instead of
  idling. The gate latches so it cannot loop.

## [0.2.2] - 2026-08-06

### Added

- **`draiver --version`, single-sourced from the changelog (drv-006).** The CLI
  could not report its version — `rootCmd` set no cobra `Version` — and the two
  version records that existed (this changelog and the git tags) had already
  drifted. `internal/version.FromChangelog` now parses the topmost
  non-`[Unreleased]` `## [x.y.z]` header, `main` embeds `CHANGELOG.md` (the
  `//go:embed` must live in the repo-root package, as it cannot reach a parent
  dir from `cmd/` or `internal/`) and hands it to `cmd.SetVersion`. That sets
  `rootCmd.Version` — cobra gives `--version` for free — and the value the
  long-running banners prefix their startup line with, so `ctl up` and `webui`
  now report `draiverctld 0.2.2 up — …` / `draiver 0.2.2 webui …` from the same
  source. The reported version *is* the changelog's latest released section; the
  two cannot drift by construction.

- **Session-liveness dot on dashboard cards (`draiver webui`, drvweb-001).** Each
  attempt card gains a coloured runtime-liveness dot: eucalypt green (agent
  running — live pid), wattle gold (stopped — session exists, dead pid, still
  enabled), ghost-gum grey (disabled — dead pid, not enabled). A never-run or a
  Stuck attempt shows no dot (its rust-red signals already carry it). The
  read-only web server does its own signal-0 pid probe (mirroring
  `reconcile.OSProc.Alive`) and reads `session.json` directly — never
  `session.Open`, which would `MkdirAll` a session dir per attempt. The
  Australian-bush colours are centralised in `internal/web/palette.go`: favicon
  SVGs stay static behind a drift test, and the dot colours reach the browser as
  `:root --dot-*` custom properties rendered from the constants, so `style.css`
  carries no dot hex.

### Changed

- **Attempt-log timestamps render as relative age, full stamp on hover
  (`draiver webui`, drvweb-002).** The attempt timeline showed each event's
  timestamp as a raw minute-precision UTC string (`2006-01-02 15:04Z`), forcing
  readers to mentally diff wall-clock strings and discarding sub-minute ordering.
  Each entry now renders a relative age instead — "just now", "3 minutes ago",
  "yesterday", … — inside a `<time>` element whose native `title` tooltip carries
  the precise second-precision UTC stamp alongside the operator's local time.
  `static/reltime.js` recomputes the age from the `datetime` attribute on load,
  after every htmx log-swap, and on a 30s tick, so the age stays live even on a
  Done attempt whose log region never polls. The Go `relativeAge` and the JS
  share one set of bucket boundaries, each pinned by a test
  (`TestRelativeAgeBuckets`, `TestTimelineShowsRelativeTimestamps`).

- **`ctl status` hides Done attempts by default; `--all`/`-a` includes them
  (`draiverctl`, drvctl-018).** The live view printed one row per attempt,
  terminal Done ones included, so as closed tickets pile up they drown the
  Running/Stuck/Review attempts an operator actually cares about. The default
  list now shows only non-Done attempts; `--all`/`-a` restores the full list, and
  an explicitly named target (`status <ticket[@attempt]>`) still prints its match
  regardless of state. When the default filters everything away, a one-line hint
  reports the hidden Done count and the `--all` opt-in instead of blank output.
  Scope is `ctl status` only; the web board (`draiver status`) is untouched.

- **`--repo` is required at attempt creation; a repo-less admit escalates instead
  of silently stalling (`draiverctl`, drvctl-017).** Two gaps around a missing
  repo, closed at both ends. **At creation:** `attempt.Create` trims and rejects
  an empty repo — the one chokepoint every creation path funnels through — so no
  path can mint a repo-less attempt; `new` and `attempt new` validate up front
  (before any side effect, no orphan dir), while `attempt new --from` still
  inherits its parent's repo (drvctl-015), so `--repo` is required only when it
  can't be inherited. **At admit:** a repo-less admit now appends an `escalation`
  event, flipping the attempt to Needs-me so it lands on the board with an
  actionable ask, rather than staying Running+enabled and stalling silently in the
  operational log. The tick never fails as a whole — siblings keep admitting — and
  every other admit failure stays log-only.

## [0.2.1] - 2026-08-05

### Added

- **`ctl logs` reads for a human by default; raw stream-json moves behind
  `--json` (`draiverctl`, drv-003).** `ctl logs` used to dump the raw
  `stream.jsonl` tee — one dense JSON object per line, the assistant's prose
  buried as an escaped Markdown string amid event uuids and the session id. It now
  renders the recorded stream the way the live `start`/`restart` view does:
  assistant prose as prose, `> tool` calls with a short argument snippet, tool
  errors, permission prompts, usage/cost + context-window fill, and turn
  boundaries — with the transport envelope dropped. The live printer and `logs`
  share one renderer (`renderEvent` in `cmd/ctl.go`), reached by normalizing each
  recorded line back through the adapter's exported `claudecode.Normalize`, so the
  two speak the same vocabulary. The raw byte-for-byte tee is still one flag away —
  `ctl logs <target> --json` — so `| jq` pipelines and replay keep working, and
  `-f`/`--follow` works in both modes.

## [0.2.0] - 2026-08-05

### Added

- **First-class Review → Running reopen (drv-002).** A reviewed attempt can be
  sent back to active work directly: logging a **`decision`** against an attempt
  in **Review** reopens it to **Running**, the decision's own text recording why.
  `decision` is now a lifecycle event in `project.Derive` — as the latest
  lifecycle marker it displaces the prior `review`, so the state falls through to
  Running; decisions logged during ordinary work keep an already-Running attempt
  Running, so the rule is invisible except when it reopens. This removes the old
  escalate-then-resolve workaround (which abused the human-question channel just
  to nudge the state) — the reopen is now a named action, recorded with a reason a
  fresh `brief` can read, and the board and per-attempt state projection show
  Running afterward.

- **Context-window auto-stop — a backstop that halts a runaway session
  (`draiverctl`, drvctl-012).** A session that grows its context window without
  bound keeps burning tokens (and money) until a human notices; `internal/limit`
  makes the supervisor stop it on its own. It is a third enforcement seam beside
  the protocol and permission gates: on every metered usage frame it measures the
  live context-window fill against a configured threshold, and the first crossing
  records a durable **`escalation`** (why + the numbers) then reaps the session.
  Recording an escalation — rather than a note — is deliberate: it flips the
  attempt to **Needs-me**, so the daemon parks it for a human instead of
  re-admitting it straight back into the same runaway (Tier 0 has no respawn
  ceiling); a later `resolve` resumes it from a fresh cold-start brief, i.e. a
  clean context window. Wired through the shared `dispatch`, so both `ctl up` (the
  daemon) and the foreground `ctl start`/`restart` enforce it. The threshold is
  configurable with a sensible default (**150,000 tokens**) via a new operator
  **config file** (`internal/config`): JSON at `~/.draiver/config.json` (override
  `--config` / `$DRAIVER_CONFIG`), a missing file is not an error (defaults
  apply), and precedence is explicit flag > config > built-in default. New flags
  `--context-limit` (0 disables) on `up`/`start`/`restart` and a `--config` /
  `--context-window` pair that now also default from the config file.
  **Measurement fix (the load-bearing prerequisite):** verifying the "~2M-token
  context" reported on the drvctl-009 run showed it was an *artifact*, not a real
  window — the terminal `result` stream-json line's usage is **cumulative across
  the whole turn** (its `cache_read` alone summed to 1.9M), and the adapter was
  running it through the per-request `ContextTokens = input + cache_read +
  cache_creation` formula. The real per-message context peaked at ~135K. So
  `claudecode.normalizeResult` now zeroes the result line's `ContextTokens` (it is
  not a context snapshot) and `watch.meter()` folds a zero-context frame as
  **cost-only**, leaving the live gauge fed solely by per-request frames — without
  which any threshold below 2M would have tripped at the first turn end and killed
  every session (drvctl-012).
- **Reconcile loop — the draiverctld daemon that ties drvctl-001–007 together
  (`draiverctl` Tier 0).** `internal/reconcile` is the supervisor's active half:
  a **declarative** control loop that each tick reads the *desired* set (attempts
  whose derived control state is `Running` — Needs-me/Review/Done are not desired)
  and the *actual* set (sessions this daemon runs, plus live processes it
  re-adopted), and closes the gap. Four moves. **Admit** — a desired attempt with
  no live session gets one: `manage.Handle` Spawns a fresh session, or **Resumes**
  the surviving cattle handle when a `session.json` is already on record; a single
  ingest goroutine is attached to the stream and the cold-start brief is injected.
  **Watch** — that goroutine tees every line to the journal, meters
  tokens/cost/context, and promotes semantic events into the durable log
  (`internal/watch`). **Gate** — the same goroutine runs both enforcement seams on
  each tool-permission callback, protocol → permission (withhold-until-logged,
  then allow-or-escalate-and-halt), so they never double-answer. **Retire** — an
  attempt that left the desired set is stopped: a terminal one (Review/Done) has
  its worktree cleaned, a blocked one (Needs-me) is **parked** with its worktree
  kept warm — so a human `resolve` flips it back to `Running` and the next tick
  re-admits it via Resume with no work lost (escalate → resolve → resume through
  the reconcile diff alone, no path-activation needed at Tier 0). **Re-adoption on
  restart** — `Adopt` rebuilds the view from disk (`session.json` per attempt) plus
  a scan of the process table: it reconciles worktrees, re-adopts every recorded
  session whose pid is still alive so a restart does **not** spawn a duplicate into
  a live agent's worktree, and leaves dead sessions to be resumed on the next tick.
  Adopting a live pid without re-streaming it is deliberate — the `agent.Adapter`
  seam has no attach verb (that is the `Runtime.Attach` seam, Tier 1+), and a ctld
  restart must not reap healthy agents; `daemon-reexec`, not a fleet bounce. A
  `Proc` seam (signal-0 liveness + SIGTERM) makes re-adoption deterministically
  testable. The client surface lands as `draiver ctl up` (run the loop until
  interrupted; Ctrl-C **drains** — detaches without reaping, so the fleet survives
  the daemon) and `draiver ctl status` (the desired/actual board). Respawn policy,
  the progress watchdog, StartLimit ceilings, budgets, the dependency DAG, and
  instant path-activation are all Tier 1+ and deliberately out of scope. Also adds
  `manage.Handle.Decide`, the passthrough the two gates use to answer the live
  adapter's permission callback (drvctl-008).
- **Protocol gate — enforce the log by process control, not agent goodwill
  (`draiverctl` Tier 0, reconcile loop step 3).** `internal/protocol` is the
  supervisor's second gate, alongside the permission gate: it enforces the
  draiver protocol the onboarding skill can only ask for. Two halves. **Cold-start
  brief** — `InjectBrief` builds the attempt's `brief.Build` and `Prompt`s it into
  a freshly spawned (or resumed) session behind a short supervisor preamble, so an
  agent is *always* briefed rather than trusted to fetch it (`manage.Handle`
  satisfies the `Prompter` seam). **Withhold-until-logged** — `Gate.Consider`
  intercepts the agent's tool-permission callback and refuses to forward a
  mutating tool until a `decision`/`gotcha` justifying it is on disk: the
  rationale reaches the durable, hash-chained log *before* the edit it explains
  does. The gate is a **veto, not an approver** — it only ever denies (withholds)
  and returns `Cleared` without answering when a tool is `Free` or already
  justified, so it composes as protocol → permission with no double-answer;
  allowing/escalating stays the permission gate's job. A withhold is transient,
  not an escalation: the session stays live so the agent logs its rationale and
  retries the same edit — nothing is halted, no escalation event is written. A
  justifying event must be **newer than the session baseline** (the log tail seq
  snapshotted at `New`), so each driven session justifies its own edits rather
  than riding a stale rationale. A layered `Policy` (`Layer(global, project,
  ticket)`, mirroring the permission gate) with an `Edits()` base marks
  `Edit`/`Write`/`NotebookEdit` `Justified` and leaves everything else `Free` —
  `Bash` deliberately stays `Free` (the agent runs `draiver log` *through* it, so
  gating it would deadlock) and remains gated to a human by the permission gate's
  `ReadOnly` base instead (drvctl-007).
- **Cattle handle — kill/keep a session id and `--resume` a fresh process
  (`draiverctl` Tier 0, hook 1).** `internal/manage`'s `Handle` binds one
  attempt's agent adapter, its git worktree, and its persisted `session.json`
  identity into a single live-session controller — the "agents are cattle"
  mechanic in one type. `Spawn` creates the worktree, starts a fresh agent
  process, and persists its session id (with pid, worktree, birth time). `Kill`
  reaps the process but **keeps the session id and the ticket log**, clearing only
  the now-stale pid — context lives in the log, not the dead process. `Resume`
  reloads the session id from `session.json` and spins a fresh process with the
  agent's `--resume`, re-attached to the same worktree (`Create` re-attaches to
  the surviving branch if a restart swept the checkout); it keeps the id,
  worktree, and birth time and refreshes only the pid. Each `Spawn`/`Resume`
  yields a new process, so `Stream()` returns a fresh channel for the caller to
  re-attach its `Watcher` to. `manage` owns identity only; the meter and stream
  tee stay with `internal/watch`. pid is read via an optional `PID()` accessor
  (added to the Claude Code adapter) so the core `agent.Adapter` seam stays the
  five control verbs (drvctl-005).
- **Stream ingest — the "Watch" step (`draiverctl` Tier 0).** The reconcile
  loop's step 2: a `Watcher` consumes one session's normalized event stream and
  does its two jobs (`internal/watch`). **Promote** — semantic protocol events
  the agent emits (gotcha / decision / escalation / review) are recognized in the
  stream and appended to the attempt's durable, hash-chained log via
  `internal/ticketlog`, so the supervisor is the single writer. A `Recognizer`
  seam with a reference `ProtocolRecognizer` lifts the `draiver log|escalate|
  review` calls the onboarding skill teaches — no new agent-facing protocol —
  and refuses (rather than corrupts the log) on any command whose body it cannot
  reproduce losslessly (command substitution, heredocs, pipes/redirects).
  **Meter** — every raw stream-json line is teed once to `stream.jsonl` (deduping
  the sibling events one line fans out into), and token/cost/**context-window**
  usage is folded into `meter.json`: context tracks the latest snapshot as a live
  gauge while cost is folded monotonically so it never regresses on the zero-cost
  per-message frames between turn ends. Promotions advance the meter's
  progress-watchdog heartbeat (drvctl-004).
- **Worktree-per-session lifecycle (`draiverctl` Tier 0).** The isolation
  primitive Admit/Retire depend on: each session gets its own git worktree, so
  parallel sessions on one repo never collide and competing attempts stay
  comparable (`internal/worktree`). A `Manager` exposes the three reconcile-loop
  verbs — `Create` on spawn (idempotent and crash-safe; re-attaches to an
  existing per-attempt branch so a stopped attempt can resume), `Remove` on
  retire (checkout only by default; opt-in branch deletion), and `Reconcile` on
  daemon restart, which sweeps worktrees left orphaned by a crash and any whose
  attempt is no longer wanted. Checkouts live under a per-repo managed base
  outside the repository (see drvctl-010) on a `draiver/<ticket>/<attempt>`
  branch; the Manager holds no state, re-deriving the truth from `git worktree
  list` each call (drvctl-003).
- **Session runtime store (`draiverctl` Tier 0).** A per-session `session/`
  directory under an attempt — `session.json` (identity: adapter, model, session
  id, pid, worktree, started), `meter.json` (cumulative token/cost/context usage
  plus watchdog counters), and `stream.jsonl` (the raw stream-json tee). It is
  rebuildable and deliberately kept **out of the hash-chained log**: whole-file
  records are replaced atomically (temp-file + fsync + rename) and `stream.jsonl`
  is append-only and safe for concurrent appends (`internal/session`, with paths
  in `internal/store`).

### Fixed

- **Permission callback now actually activates, so `ctl start` sessions can do
  work (`draiverctl` Tier 0, drvctl-013).** The permission gate could never fire:
  headless `claude` was launched without telling it to route tool-permission asks
  to the client, so the first `Write`/`Edit` an agent attempted was auto-denied
  by `claude` itself (*"…you haven't granted it yet"*) and the attempt stalled —
  the request never reached draiver's gate. (This surfaced only after drvctl-010
  moved checkouts out of `.git`, uncovering the next, more fundamental block.) The
  fix passes `--permission-prompt-tool stdio` on every session, which routes asks
  over the stdio control protocol as `control_request{can_use_tool}` frames — the
  seam `internal/gate` and `claudecode.Decide` were built for. The flag is
  undocumented (dropped from `claude --help`) but live in `claude` 2.1.x; verified
  against 2.1.216. The supervisor now also defaults `--permission-mode` to
  **`acceptEdits`**: `claude`'s path-aware classifier auto-approves edits *inside*
  the worktree (an attempt progresses without a human waving through every write),
  while writes *outside* the checkout and other risky tools are still routed to
  the gate and escalate. Covered by a new live gated-tool case in
  `manual_smoke_test.go` (in-workdir write flows; out-of-workdir write hits the
  gate seam and, denied, does not land).

- **Worktree checkouts no longer live under `.git` — they stalled the agent's
  auto-mode classifier (`draiverctl` Tier 0).** The managed base defaulted to
  `<git-common-dir>/draiver/worktrees/...`, putting every attempt's checkout —
  the agent's working directory — *under* `.git`. A coding agent's auto-mode
  permission classifier treats any path under `.git` as protected and refuses to
  write there, so bringing up an attempt's isolated workspace during `ctl start`
  stalled. `worktree.NewManager` now derives the default base *outside* the
  repository — `<user-cache-dir>/draiver/worktrees/<repo-label>-<hash>` — keyed by
  a hash of the git-common-dir so it stays deterministic per repo and the
  stateless Manager re-computes it after a restart. This preserves the original
  goal (checkouts stay out of the tracked working tree — no `.gitignore`, no
  accidental commits) while also keeping them out of `.git`; the fix relocates the
  workspace rather than touching any gate, so genuine writes into a real `.git`
  are still classified as unsafe. The per-attempt branch lives in the repo's refs,
  so a checkout swept from the cache is recreated by re-attaching to it
  (drvctl-010).

## [0.1.1] - 2026-08-04

### Added

- **MIT license.** The project is now released under the MIT License
  (`LICENSE`), with a License section linking to it from the README.

## [0.1.0] - 2026-08-03

First tagged release. Everything built to date is collected here.

### Added

- **Durable ticket core.** An append-only, hash-chained event log with an on-disk
  store layout: each event carries its predecessor's hash, so any edit breaks the
  chain. `draiver audit` verifies the chain and exits nonzero if tampered.
- **CLI protocol verbs.** The agent/human protocol as commands — `new`, `log`,
  `escalate`, `resolve`, `review`, `done` for writes, and `brief`, `inbox`,
  `audit`, `status` to replay context and project state.
- **Per-attempt schema.** A ticket holds one or more independent *attempts*, each
  owning its own log, hash chain, and control state, so different tools or models
  can work the same `spec.md` and be compared and audited separately.
- **Read-only HTMX web board** (`draiver webui`) over a data folder: the
  four-column control-state board plus a per-ticket attempt detail view with
  markdown-rendered log bodies.
- **Onboarding skill for agents**, packaged as a Claude Code plugin marketplace
  under `./.claude` with a supported install path (`/plugin marketplace add` +
  `/plugin install`) (task-003).
- **Playwright end-to-end tests** covering the read-only web UI.
- **Markdown log bodies.** The web UI renders each log event body as sanitized
  markdown (script and unsafe links stripped); bodies are still stored as raw
  text, so the hash chain and `audit` are unaffected (task-004).
- **Branded favicon with a live badge.** The board serves a eucalypt-green "D"
  favicon that gains a rust-red badge whenever any attempt is Stuck (task-005).
- **Breadcrumb on the attempt detail page** — a `board › attempts › <attempt>`
  trail, so both the board and the attempt list are one click away (task-007).
- **Live attempt detail page.** The log timeline and status badge auto-refresh
  via htmx polling and stop polling once the attempt is terminal (the live
  fragment answers `286` to self-cancel); the whole-board poll, by contrast,
  never quiesces, since a new ticket or attempt can appear at any moment
  (task-008, task-009).
- **Log-ordering toggle** on the attempt detail page, switching between
  newest-first and oldest-first (task-002).
- **Deep links to a log entry.** Every event has a stable, shareable URL
  (`/ticket/{id}/{attempt}#event-{seq}`) with a copyable permalink that scrolls
  to and highlights the entry; the highlight survives the live log poll and the
  order toggle, and Stuck/Review cards link straight to the open escalation or
  review claim (task-011).
- **`draiver title`** command to set or update a ticket's `spec.md` title as
  metadata, outside the hash-chained log.
- **Mandatory ticket titles.** `draiver new` refuses a blank/whitespace title,
  and a `--spec` import can carry its title via `title:` frontmatter (task-012).

### Changed

- **Board columns reordered and relabeled** to `Running | Stuck | Review | Done`;
  the column formerly named "Needs me" is now "Stuck" (task-001).
- **Attempt log defaults to newest-first** in the detail view, reversing the
  previous oldest-first default (task-002).
- **Onboarding skill restructured** from a loose `skills/` folder into the
  `./.claude` plugin marketplace, giving one canonical, installable location
  (task-003).
- **Favicon badge decoupled from the DOM.** The Stuck badge now flips off an htmx
  `HX-Trigger` event carrying the board state, instead of scraping a
  `data-testid` count out of the page (task-010).

### Fixed

- **Onboarding skill corrected** to match the intended protocol: agents escalate
  to ask and then stop, the human records the resolution, and there is no
  mid-session re-brief. Documented the "agents are cattle" respawn model.
