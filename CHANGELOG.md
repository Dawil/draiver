# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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
