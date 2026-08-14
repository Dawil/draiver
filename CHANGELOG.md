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
