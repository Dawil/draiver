# The test pyramid — a generic, loosely-coupled verifier

> Draiver already couples to **code forges** without knowing any one forge: it
> reads `git remote`, builds a compare URL, and proves a merge by *containment*
> (`draiverctl.md`'s `merge --remote`), never by calling GitHub's or Forgejo's API.
> This document proposes the same move for **testing**. A repo declares *how it is
> tested* in a checked-in `.test-pyramid.yaml`; draiver learns to *climb* that
> pyramid deterministically before a ticket claims Review — without hardcoding
> `go test`, `pytest`, `docker compose`, or any one test runner. It is the
> verification twin of the forge coupling, and it lands on the exact trust seam the
> platform docs say is load-bearing.

## The idea

A ticket's attempt should reach **as high as it can — or as high as it is required
to** — up its repo's test pyramid before it raises for Review. Today "all tests
green" is a phrase the agent *types into its review claim* (`handbook.md` §7:
*"all tests green; covers the spec's three acceptance criteria"*). That is exactly
the self-report the platform's trust model says it structurally **cannot** trust
(`coding-agent-platform.md`: *"Success is a claim, not exit 0 … the platform's
trust model cannot extend to the worker's self-report"*).

The pyramid is also where a ticket's **acceptance criteria** become enforceable: a
spec authors them as plain prose, the attempt's agent realises them as Gherkin
features + steps, and those run as a BDD rung climbed here — see
[`acceptance-criteria.md`](./acceptance-criteria.md).

The test pyramid replaces the phrase with a **fact draiver itself established**: a
rung draiver *ran* and *adjudicated*, recorded as a structured claim on the
attempt. The pyramid is declared in the repo (loose coupling — the repo owns *how*
to test itself), but it is **executed and scored by the deterministic spine** (the
`draiver`/`ctld` binary), so the recorded rung is trustworthy no matter who
triggered the run.

The list is **ordered bottom-to-top**: it *is* the pyramid. Draiver climbs from
the base; the highest rung it *cleared* becomes the attempt's verification claim.

## Settled MVP (v1)

The design was deliberately narrowed to the smallest slice that delivers the core
value — **integration tests actually run before a Review is raised**. Unit tests
are near-universal (`make test`); the unstandardised part is *bringing up an
integration environment*, which is precisely what a repo captures in its file. The
four decisions below are **locked for v1**; the richer vocabulary later in this doc
(`up`/`down`/`healthcheck`, per-ticket floors, the coordinator tie-in) is
explicitly **deferred**.

```yaml
# .test-pyramid.yaml — repo root, checked in, versioned with the code (v1 schema)
levels:
  - name: unit
    run: go test ./...
  - name: integration
    run: docker compose up -d db && go test -tags=integration ./... ; docker compose down -v
```

1. **Draiver executes and records; the agent decides when to climb.** `draiver
   test` runs the file's commands and writes the result, so the verdict is
   trustworthy (draiver observed the exit code) — not a self-report. The agent
   keeps full agency over *when* and *how high* to climb. This is the
   deterministic-spine / stochastic-leaf line drawn exactly where it belongs.

2. **Target rung = the top rung declared in the file.** The file author declares
   the bar by what they put at the top (integration, in the example). There is **no
   separate `test_level` frontmatter field and no config default in v1** — the file
   is the single source of truth. **A missing `.test-pyramid.yaml` is a valid,
   non-error state**: no file → no gate.

   > **Superseded by drv-025.** The gate is no longer the single top rung — it is
   > the set of rungs marked `required` (default true). See
   > *"Per-rung `required`"* below.

3. **`--log` gates on a clean tree.** Bare `draiver test` runs against a dirty tree
   (fast, iterative verification — records nothing). `draiver test --log` **refuses
   if the working tree has uncommitted changes**, then runs. So the agent iterates
   dirty, commits when confident, and *then* logs a result against a real commit.

4. **Only green results are logged.** A run that clears the requested rung writes
   **one hash-chained result event `{rung, commit}`** (the HEAD SHA). A failing run
   **logs nothing** and exits nonzero. The log therefore holds only passing results
   by construction, which makes the fold trivial: *the highest rung with a logged
   result at current HEAD.* A result whose `commit` ≠ HEAD is **stale** and does not
   count — a further commit after logging correctly invalidates the claim.

**The Review gate.** `draiver review` folds the log over the **required set**: every
rung marked `required` (the default) must be logged green at current HEAD. If a
`.test-pyramid.yaml` exists but some required rung is not green at HEAD, the claim is
**refused** with a message naming the outstanding required rung(s) and the fix
(`draiver test --log`). No file → no gate (not a failure). This is the
enforce-by-process-control seam, the same shape as the permission gate and
`merge`'s clean-checkout gate. It does not remove the human Review gate — it
shrinks the judgment surface behind it. `brief` surfaces the result for free (it is
a replayed log event), so a fresh agent inherits "acceptance green @ abc123."

> **Pre-drv-025 this was a single top rung** ("the highest logged rung at HEAD
> equals the target"). drv-025 generalised it to the required set so an advisory
> rung can sit anywhere in the ladder without gating. See *"Per-rung `required`"*.

**The v1 verb:**

```
draiver test [RUNG] [--log]
```

- Loads the pyramid from the attempt's **worktree** (the code under test). No file
  → nothing to do, exit 0 with a note.
- Climbs base→`RUNG` (default: the target/top rung), running each rung's `run` in
  the worktree and streaming child output; stops at the first non-green rung.
- Bare: run + report on any tree, records nothing. `--log`: refuse on a dirty tree;
  on all-green through the requested rung, append the `{rung, commit}` event.

Draiver reads HEAD + tree-cleanliness from the worktree itself (reuse the
`worktree`/git helpers `merge`/`sync` already use). The agent's loop mirrors how it
already runs `git` (`handbook.md` §5, *"exactly as you run `git commit`
yourself"*): iterate with `draiver test`, commit, then `draiver test --log` before
`review`.

**Dogfooding.** `drv-010` adds a `.test-pyramid.yaml` to *this* repo whose
integration rung stands up real dependencies via [Testcontainers](https://testcontainers.com/)
— the first real consumer of the feature.

## Environments (drv-012): named up/down/healthcheck contexts

The deferred "fuller vocabulary" below (`up` / `down` / `healthcheck`) is now
**realised, reorganised around named environments** rather than per-rung inline
keys. A rung does not run in a vacuum — it runs *in a context* — so
`.test-pyramid.yaml` gains a top-level `environments:` list, a sibling of `levels:`,
and a level names the one it targets:

```yaml
environments:
  - name: local
    # up/down optional — a local env may need neither
    healthchecks:
      - name: postgres-up
        script: pg_isready -h localhost
  - name: staging
    # up/down are script strings, executed via `sh -c` exactly like a level's
    # `run:` — not a path draiver points at. Inline the command (or `sh -c` a
    # file yourself if you keep one).
    up:   docker compose -f staging.yml up -d      # optional
    down: docker compose -f staging.yml down -v    # optional
    healthchecks:
      - name: api-reachable
        script: curl -fsS https://staging.example.com/healthz

levels:
  - name: unit
    run: go test ./...
  - name: integration
    run: go test -tags=integration ./...
    environment: local                  # <-- rung names its context
  - name: e2e
    run: go test -tags=e2e ./...
    environment: staging
```

- **Why a separate list, not per-rung inline keys?** One staging/QA context is
  shared by several rungs (integration *and* e2e both hit staging) — define it once,
  reference it by name. And it separates *what the code is* (a level's `run`) from
  *where it runs* (an environment), the same own-it-vs-observe-it line the forge
  coupling draws between `up` (an env draiver owns) and `healthcheck` (one it only
  observes).
- **Binding.** A `Level` gains an optional `environment:` naming one entry. **Unset
  = the implicit ambient context** (today's behaviour), so existing files keep
  working. A level naming an unknown environment is a validation error.
- **Backward compatible.** A file with no `environments` behaves exactly as before;
  the parser already ignores unknown keys, so old binaries tolerate new files and
  new binaries tolerate old files.

**Execution order** for a rung with an environment (`draiver test`): run the env's
`up` → run its `healthcheck`s → run the level's `run` → **always** run `down`
(unconditionally, pass or fail; a `down` failure is a best-effort warning, never a
verdict change). Each failure maps to the control outcome the vocabulary settled,
surfaced as a distinct process exit code so a supervising loop can branch on it:

| Failure | Meaning | Control outcome | Exit |
| --- | --- | --- | --- |
| `up` non-zero | the harness could not stand the env up | infra fault, **not** your code → **escalate** | `6` |
| `healthcheck` red | a dependency the env only observes isn't ready | **block** (the pass-the-ball signal) — not a code fault | `7` |
| `run` non-zero | the code is wrong at this rung | code fault; the ceiling is below this rung | `1` |

This ticket only *emits* the block signal; wiring a red `healthcheck` into the
coordinator's reverse-`wants:` activation stays deferred (below).

**The realistic dogfood pattern.** A `local` environment whose healthcheck is a
container-runtime probe is the honest shape:

```yaml
environments:
  - name: local
    healthchecks:
      - name: container-runtime
        script: docker info >/dev/null 2>&1 || podman info >/dev/null 2>&1
```

On a box with no runtime this **blocks** the integration rung (dependency not
ready) rather than letting a clean-skip report a false green. *This* repo's own
`.test-pyramid.yaml` deliberately uses a weaker always-green precondition
(`go version`) instead, because its integration suite already skips cleanly without
a runtime and we do not convert that clean-skip into a gate-blocking block as a side
effect of landing environments.

**Deferred within environments.** Secret/credential injection for `up` /
`healthcheck` (Resolved-forks #4 below) is out of scope — don't hand staging creds
to every worktree; that is a follow-up. The webui surfaces *which* environment a
rung targets, but not a live healthcheck verdict (only green `run` results are
logged, so no probe outcome is cheaply available to the read-only board).

## BDD / acceptance rung (drv-016): cucumber-JSON is the interchange

An acceptance rung is **not a new rung kind** — it is an ordinary rung that (a)
binds an `environment:` (drv-012) and (b) declares the **cucumber-JSON** report its
`run` emits, via `cucumber_json:`. That one field is the whole convention:

```yaml
environments:
  - name: staging
    up:   docker compose -f staging.yml up -d
    down: docker compose -f staging.yml down -v
    healthchecks:
      - name: api-reachable
        script: curl -fsS https://staging.example.com/healthz

levels:
  - name: unit
    run: go test ./...
  - name: acceptance
    environment: staging                       # up → healthcheck → run → down
    run: godog run -f cucumber:report.json ./features   # the repo's BDD runner
    cucumber_json: report.json                 # where it writes cucumber-JSON
```

- **draiver never hardcodes a runner.** The `run` is the same generic `sh -c`
  shell-out every rung uses; it invokes *whatever* BDD tool the repo configures
  (godog, cucumber-js, behave, pytest-bdd, …). draiver consumes the **JSON, not the
  tool** — so the rung is runner-agnostic by construction.
- **`cucumber_json:` names the report**, relative to the worktree root. After the
  `run`, draiver reads it, parses it (`internal/cucumber`), and prints a one-line
  summary — proving the report is present and consumable. Capture runs **whether the
  run passed or failed**: a red run's report is exactly what a human Review and the
  HTML renderer (drv-018) need to see *which* scenarios broke.
- **Capture is best-effort.** A missing or invalid report is a warning that never
  changes the rung's verdict (like `down`): the `run`'s exit code stays
  authoritative. The control-outcome mapping is unchanged — a red `up` escalates
  (6), a red `healthcheck` blocks before the run (7), a red `run` is a code fault
  (1).
- **The cucumber-JSON is the interchange.** `internal/cucumber` is the shared model
  both **drv-017** (durable artefact storage of the report + its embedded
  screenshots) and **drv-018** (HTML rendering) build on. This ticket captures and
  consumes it; it deliberately does **not** define where the report is durably
  stored (drv-017) or how it is rendered (drv-018).

A BDD run on its own never substitutes for the human Review — a cleared acceptance
rung *feeds* the Review, it does not replace it (drv-014).

## Per-rung `required` (drv-025): advisory rungs the spine does not gate on

The file order conflated two orthogonal things. drv-025 splits them:

- **Altitude** — where a rung sits on the testing V (unit < integration < e2e). It is
  intrinsic to *what the test is*, and it is the **only** thing the list order
  encodes. Nothing moves in the list because it is flaky or optional.
- **Enforcement** — whether a rung must be green before `draiver review` admits a
  claim. This is a policy driven by flakiness, environment availability, and
  confidence — **not** by altitude. It is carried by a new per-rung flag.

```yaml
levels:
  - name: unit
    run: go test ./...
  - name: acceptance
    run: go run ./features/acceptance        # required by default
  - name: integration
    run: go test -tags=integration ./...
    environment: local
    required: false                          # advisory: run & reported, never gates
```

- **`required: true|false`, default `true`.** Omitting the key keeps a rung
  required, so **every pre-drv-025 file behaves exactly as before** (all rungs
  required; the gate is the top rung green at HEAD). `required: false` makes a rung
  **advisory**: still *defined*, still *run* when it lies in the climb path, still
  *reported* — but a red advisory rung (for **any** reason: a genuine failure or an
  unready environment) **never blocks the review gate and never halts the climb**.
- **Not a contiguous prefix.** A `required` rung may legitimately sit *above* an
  advisory one; the required set need not be the bottom N rungs. The gate folds the
  whole set.
- **Validation.** A pyramid with **zero** required rungs is rejected at load —
  nothing to gate on is a config error, not an ungated repo. (A repo with no pyramid
  file at all stays ungated, as always.)

**Two mechanics follow directly.**

1. **`draiver test` no longer stops at the first non-green rung unconditionally.** It
   climbs base→up and halts only at the first non-green **required** rung (with that
   rung's control-outcome exit: up=6/escalate, healthcheck=7/block, run=1/fault). A
   red **advisory** rung is reported and the climb **continues past it** — higher
   suites on the V do not depend on lower suites *passing*, and otherwise a required
   rung above an advisory one would be unreachable.
2. **The review gate folds the required *set*, not a single top rung.** Admission is
   now *"every `required` rung is logged green at this exact HEAD"* rather than *"the
   highest logged rung equals the target."* Stale results (logged before a later
   commit) still do not count. The refusal names the specific outstanding required
   rung(s).

**What `default_test_rung` (per-repo `TestRung`) becomes.** Its two drv-011 jobs
split, and only one survives:

- Its **review-gate floor** role is **removed** — the file's `required` flags are now
  the sole authority on what the spine requires (a scalar cannot express a floor over
  a non-monotonic required set anyway).
- Its **`draiver test` default-climb** role **survives, clarified**: it is purely the
  **iteration ceiling** for the fast local loop — how high bare `draiver test` climbs
  when no explicit `RUNG` is given. And the default itself changed: bare `draiver
  test` now climbs to the **highest `required` rung** (what review needs), not the
  file's top rung. `TestRung` narrows that ceiling *down* (faster loops); an explicit
  `RUNG` argument still climbs *up* into advisory rungs on demand.

**This repo dogfoods it.** `.test-pyramid.yaml` marks `integration` `required:
false` (it needs a container runtime and already skips cleanly without one), so
draiver's own gate requires `unit` + `acceptance` and treats `integration` as
advisory — altitude keeps it on top of the V, enforcement makes it non-blocking,
without corrupting the ordering.

**Deferred (built only on demand).** *Per-environment opt-in enforcement* ("advisory
in the file, but the CI box that *can* run it treats it as required") would be an
explicit **promote-only** operator setting (e.g. `additional_required: [e2e]`) that
can only *add* enforcement over the file author's rungs, never remove it — preserving
the trust boundary (the gate must not be *softenable* from mutable `config.json`). It
must **not** overload the `TestRung` scalar (now the iteration ceiling). Renaming
`required` to `blocking:`/`gates_review:` was considered and declined: the subtlety
that an advisory rung is still *run* (to climb past it) is documented here rather than
encoded in the name.

## Overlays (drv-041): the pyramid from a separate, developer-owned repo

`.test-pyramid.yaml` (and the `features/`, review-env scripts, and unit tests it
drives) is partly a *development-environment* feature. A team that has not adopted
draiver should not be forced to carry those files in their shared repo. drv-041
lets a **single, local-first developer** keep their own test tooling in a
**separate, developer-owned git repo**, pointed at **only via `config.json`** —
nothing is added to the target repo's version control.

**Sharing with a team is explicitly out of scope.** If tooling is to be shared,
the simpler answer is to colocate it in the one repo; the overlay is personal dev
tooling, not a team-distribution mechanism. A backup remote on the overlay is
optional and entirely the developer's business — draiver never provisions it.

The overlay is itself a repo so its per-attempt worktrees stay isolated and run in
parallel, exactly like the codebase worktrees draiver already cuts.

### Resolution model — in-repo always wins

For a codebase at local path `P` (the attempt's `repo`), the pyramid source is
resolved in order (`internal/overlay` `Resolve`, a pure function with no side
effects; step 1 is the caller's cheap `pyramid.Load` probe):

1. **In-repo pyramid wins.** If the codebase worktree has a `.test-pyramid.yaml`,
   use it and ignore overlays entirely — today's behaviour, unchanged. There is
   **no layering** of an overlay on top of an in-repo pyramid.
2. **Explicit per-codebase overlay.** Else, if `repos.<P>` carries an explicit
   `overlay` path, use that repo. It is **not** existence-checked at resolve time:
   a mispoint must fail loudly when the worktree is cut, never silently fall
   through and mask the misconfiguration.
3. **Global default folder.** Else, if `overlay_dir` is set and
   `<overlay_dir>/<codebase>` exists and is a git repo, use it. A missing or
   non-git candidate cleanly falls through, so `overlay_dir` may cover only some
   of a developer's repos.
4. **None.** Else no pyramid — `draiver test` stays a successful no-op, exactly as
   a repo with no pyramid file always has.

`<codebase>` for the folder lookup is the codebase repo directory's **basename**.
The explicit per-codebase `overlay` setting is the escape hatch for a basename
collision (two repos both named `api`).

### Settings (`config.json`, outside version control)

No new file — the overlay knobs live in the existing operator config
(`internal/config`; `~/.draiver/config.json` by default):

- **Global `overlay_dir`** — a folder of developer-owned overlay repos, looked up
  by codebase basename. A new top-level `Config` key, written global-only via
  `config.SetOverlayDir` (merge-preserving `atomicWrite`, the same read-modify-write
  the per-repo settings use).
- **Per-codebase `overlay`** — an explicit overlay location on `RepoSettings`,
  keyed by the codebase path under `repos.<path>`, the twin of the existing
  `review_env` / `default_remote` fields. Resolved via `Config.RepoSettings` →
  `Resolved`; partial-edited via `RepoSettingsUpdate` (`*string`, nil = untouched).
  Set at attempt time but **persisted to the codebase's settings** so it sticks for
  later attempts (`SetRepoSettings` → `atomicWrite`).

### Ownership & worktree model — operated in place

**Developer-owned, operated in place — draiver does NOT clone or manage a mirror.**
`internal/overlay` `Cut` materialises the overlay as a per-attempt worktree with
the same `git worktree add` pattern the codebase and review-env worktrees already
use (`internal/worktree`). The developer lives in and hand-edits the overlay repo
directly; draiver never buries it in the cache dir.

- Baseline is the overlay repo's **default branch** (track-default, not a pinned
  ref — the overlay repo is operated in place, so its HEAD *is* that default
  branch). A writable attempt cuts `draiver/<ticket>/<attempt>` from it, in its own
  worktree under the cache dir, mirroring the codebase branch so parallel attempts
  stay isolated.
- Overlay edits an attempt makes land on `draiver/<ticket>/<attempt>` in the
  overlay repo, **never on its main** — the overlay's main is left untouched.

### Execution contract — one cwd, two env vars

The pyramid's `run`/`up`/`down`/healthcheck commands and `cucumber_json` /
`artifacts` paths resolve against **one root** today. With overlay files in a
second directory the contract is fixed as (`internal/overlay` `ScriptEnv`,
`cmd/test.go`):

- **cwd = the codebase worktree, always** (overlay or not). This is what keeps a
  local pyramid working untouched and keeps a pyramid portable between in-repo and
  overlay. `cucumber_json`/`artifacts` continue to resolve against the codebase
  root, where draiver already captures them.
- **`DRAIVER_CODEBASE_DIR`** is exported unconditionally when an overlay is active
  → the codebase worktree (the cwd): a reliable absolute anchor for a script that
  `cd`s away and needs to get back to the code under test.
- **`DRAIVER_OVERLAY_DIR`** is exported **only when an overlay is active** → the
  overlay worktree. Overlay-authored commands reach overlay assets explicitly, e.g.
  `run: go run $DRAIVER_OVERLAY_DIR/features/acceptance`.
- A local (in-repo) pyramid references **neither** variable and runs byte-for-byte
  unchanged — no cwd change, no extra environment. Backward compatible.

> **Env-var naming (open decision, resolved).** The names are
> **`DRAIVER_CODEBASE_DIR`** and **`DRAIVER_OVERLAY_DIR`** — *not*
> `DRAIVER_TARGET_DIR` / `DRAIVER_REPO_DIR`. "Codebase" reads as the plain-English
> counterpart to "overlay" at the point of use (`$DRAIVER_CODEBASE_DIR` vs
> `$DRAIVER_OVERLAY_DIR`); the provenance field stays `repo`, an internal name
> whose reuse in a shell-facing variable would be the more confusing of the two.

### `draiver test create-overlay` — deterministic opt-in

A first-class subcommand (not an ad-hoc `git init`), so a coding agent has a
**deterministic, repeatable** way to opt a codebase in (`cmd/test_overlay.go`). Run
from inside the attempt's codebase worktree, it:

- creates the overlay repo at the resolved location (explicit per-codebase path,
  else `<overlay_dir>/<codebase>`), `git init`s it, scaffolds a starter skeleton
  (a minimal `.test-pyramid.yaml` whose `unit` rung is a trivially-green `true`
  placeholder, a `features/` dir, `review-env/` `up`/`down`/`healthcheck` script
  stubs, a `README` and `.gitignore`), makes the initial commit (so the default
  branch exists for a worktree to cut from), and records the per-codebase `overlay`
  setting so resolution finds it next time;
- is **idempotent and safe**: it refuses — writing nothing — if the codebase
  already has an in-repo `.test-pyramid.yaml` (in-repo wins; an overlay would be a
  silent no-op) or if an overlay (or any non-empty dir) already occupies the
  target;
- leaves remote setup to the developer (optional backup).

> **Command placement (open decision, resolved).** `draiver test create-overlay`,
> not a top-level `draiver overlay {init,status,…}` group. v1 has exactly one
> overlay verb; it reads as a mode of `draiver test`, which is the command the
> overlay exists to serve. The top-level group is the right move only if more
> overlay verbs appear.

> **Scaffold contents (open decision, resolved).** A `features/` + `review-env/`
> skeleton, not a bare `.test-pyramid.yaml` only — the extra dirs and script stubs
> cost nothing to write and show the developer the shape of a full overlay (where
> features and review-env scripts go) rather than leaving them to rediscover it.

### Provenance

The overlay repo **and the exact commit used** are recorded in the attempt's
`attempt.md` provenance (`internal/attempt` `Meta.Overlay`/`OverlayCommit`,
`internal/repo` `Provenance`/`SetProvenance`), alongside `Repo`/`Base`, so a run or
review env is explainable after the fact — and so `draiver review` and the board
can surface the overlay even before any `draiver test` has run.

### Review behaviour — advisory, never gating

`cmd/review_overlay.go`, wired in `review.go` **after** `reviewGate` so it can only
*append* links and print — it can never turn a clean codebase review into a
refusal:

- When the attempt produced overlay changes, review surfaces a **second compare URL
  / diff** beside the codebase one. With a review remote on the overlay (reusing
  `PrimaryRemote` / `DefaultRemote`, link-validated against `ReviewLinkHosts`) it
  shows a compare URL; local-only, or an ambiguous multi-remote, degrades
  gracefully to the branch name + a `git -C <overlay> diff main..branch` hint — not
  an error.
- The overlay change is **advisory-but-loud**: it does not gate the codebase
  review, but review warns prominently that an **unmerged overlay change** exists
  and the codebase will regress on the next run without it (the two repos are
  coupled even though they are separate).
- Review runs overlay hygiene: a **dirty overlay main** gets an advisory nudge (the
  owner may have legitimate WIP) — a heads-up, not a gate.

### Web UI (`internal/web`)

- **Settings.** The per-codebase `overlay` and the global `overlay_dir` are both
  edited from the existing repo-settings panel on the attempt page — `overlay` as a
  twin of `review_env` through `SetRepoSettings`; `overlay_dir`, being global,
  through a separate `SetOverlayDir` gateway. Both round-trip through `config.json`
  without clobbering other keys.
- **Provenance** is surfaced read-only on the attempt detail header
  (`overlay: <repo> @ <shortSHA>`).
- **Review** renders the second compare URL / diff and the advisory
  overlay-dirty / unmerged-overlay warnings.
- **Badge fallback.** The branch-read path (`internal/web/pyramid.go` →
  `worktree.FileAtBranch`, via `overlayPyramid`) resolves the pyramid from the
  overlay when the codebase has none, so the badge works even without a live
  checkout. HEAD and dirtiness stay the **codebase's** — the overlay only supplies
  the rung *structure*, so the green-at-HEAD fold is unchanged.

## Why this is on-thesis, not a bolt-on

Three of the platform's core values (`coding-agent-platform.md` §"Core values")
land directly on this feature — it is not decoration, it is the trust model
growing a limb:

- **#5 Deterministic spine, stochastic leaves.** A test command is deterministic;
  its exit code is ground truth in a way an agent's turn never is. So the pyramid's
  *execution* belongs in the spine, quarantined away from the stochastic worker.
  The agent may *choose when* to climb (self-service); draiver *owns the verdict
  and the record* — the agent cannot forge an exit code it did not produce, the
  same guarantee `merge --remote`'s containment check gives.
- **#6 Success is ratified, never self-certified.** The doc lists the ratifiers
  explicitly: *"human, tests, judge, tournament."* Tests are a **structural**
  ratifier the platform has simply not wired in yet. This wires it in.
- **The trust boundary moves; it is not deleted** (`coding-agent-platform.md`
  §"two asymptotes"): *"the mechanical half of stage 3 is already absorbed; only
  the judgment of approval remains."* A green pyramid does not remove the human
  Review gate — it **shrinks the judgment surface** behind it. A reviewer who can
  see "draiver cleared unit + integration on this attempt" spends their scarce
  attention on the judgment tests can't make, not on re-running the suite.

This is the PE flywheel (`coding-agent-platform.md` §"the flywheel") applied to
verification: freeze a recurring human check ("did you run the tests?") into a
paved, self-serve rail.

## The rhyme with the forge coupling (why "loosely coupled" is the whole point)

| Concern | Forge coupling (built) | Test pyramid |
| --- | --- | --- |
| Where the capability is declared | the repo's `git remote` + config `primary_remote` | the repo's `.test-pyramid.yaml` |
| What draiver hardcodes | nothing forge-specific — generic git + a compare-URL shape | nothing runner-specific — it shells the declared commands |
| How truth is established | **containment** (`git fetch` + `--is-ancestor`), read-only, never the forge API | **exit code** of a command draiver ran itself |
| Who owns the record | `ctl merge` writes `done` = *code is in the base* | `draiver test` writes *rung N cleared @ commit* |
| The escape hatch | ambiguous remote → **escalate, don't guess** | missing pyramid → **no gate** (not a failure) |

The design rule is identical: **draiver understands the generic substrate (a git
remote; a shell command with an exit code); the repo supplies the specifics.** A
Go repo, a Node repo, and an infra repo each ship a different `.test-pyramid.yaml`
and draiver needs to learn nothing about any of them — precisely as it needs to
learn nothing about GitHub vs Forgejo today.

## Storage & the three-stores triage

| Piece | Store | Why |
| --- | --- | --- |
| Rung definitions (`name`/`run`; later `up`/`down`/`healthcheck`) | **the repo working tree** (`.test-pyramid.yaml`) | *How to test* is a property of the code, versioned and reviewed with it — like `git remote`, not like a ticket. This is the forge-coupling store, made explicit. |
| A verification run's result (`{rung, commit}`) | **the hash-chained `log/`** | A durable, attributed semantic event a `brief` must replay — it is *why* an attempt is (or isn't) reviewable. Like `escalate`/`review`. |
| Raw child stdout/stderr, timings | **`session/` tier** (`stream.jsonl` tee) | Rebuildable machine output, never the hash chain — the drvctl-027 health-log discipline. |
| *(deferred)* Required floor (`test_level:`) | `spec.md` frontmatter | Immutable design input, shared across attempts — the `depends.go` precedent. Deferred: v1 uses the file's top rung as the bar. |
| *(deferred)* Global default (`default_test_level`) | config.json | Mutable operator tunable — the `default_supervision` precedent. Deferred with the floor. |

The one genuinely new store is the first, and it is not new at all: it is the same
"read a capability declaration out of the repo" that the forge coupling already
does with `git remote`. Nothing here writes runtime state into the unit file, which
is the mistake the static/dynamic split exists to prevent.

## Implementation tickets

Filed under epic **drv-009** (*Test pyramid — run integration tests before Review
via a repo-declared `.test-pyramid.yaml`*), which `wants:` the four below. All
Tier-2/Tier-3 (`draiverctl.md`): engine = `drvctl`, UI = `drvweb`.

| Ticket | Title | Depends on |
| --- | --- | --- |
| **drvctl-047** | `internal/pyramid` — parse & validate `.test-pyramid.yaml` (ordered `{name, run}` rungs; expose the ordered list + the target/top rung; missing file is a valid non-error state). Data model only. | — |
| **drvctl-048** | `draiver test [RUNG] [--log]` — the deterministic executor: climb base→RUNG in the worktree; bare runs on any tree, `--log` refuses on a dirty tree and on all-green writes one hash-chained `{rung, commit}` result event (failures log nothing, exit nonzero). | 047 |
| **drvctl-049** | The **Review gate** — `draiver review` folds the log for the highest logged rung at HEAD and refuses a claim below the target rung; no file → no gate. | 047, 048 |
| **drvweb-021** | Board/attempt view — render the attempt's cleared rung (a small pyramid badge), distinguishing green-at-HEAD from stale (result exists but `commit` ≠ tip). | 048 |
| **drv-012** | **Environments** — a top-level `environments:` list (named `{up?, down?, healthchecks[]}` contexts) + an `environment:` binding on levels, parsed/validated in `internal/pyramid`; `draiver test` honours up → healthcheck → run → down with the control-outcome exit codes (up=6/escalate, healthcheck=7/block, run=1/code-fault); the badge surfaces the targeted env. The concrete realisation of the deferred vocabulary below. | 047, 048, drvweb-021 |
| **drv-016** | **BDD execution rung** — a rung's optional `cucumber_json:` marks it a BDD/acceptance rung: its `run` shells out to the repo's configured BDD runner (never hardcoded) over an `environment:`, and after the run `draiver test` reads the emitted **cucumber-JSON** via `internal/cucumber` (the runner-agnostic interchange), summarising it best-effort (pass or fail) without changing the verdict. Feeds drv-017 (artefact storage) / drv-018 (HTML). | 012 |
| **drv-041** | **Overlays** — resolve the pyramid from a separate, developer-owned repo pointed at only via `config.json` (global `overlay_dir`, per-codebase `overlay`), in-repo always wins; cwd stays the codebase worktree with `DRAIVER_CODEBASE_DIR`/`DRAIVER_OVERLAY_DIR` exported; `internal/overlay` cuts a per-attempt worktree; provenance records the overlay repo + commit; `draiver test create-overlay` scaffolds + registers one; review surfaces a second compare URL advisory-but-loud, never gating. Settings-only, zero version-control footprint. | 047, 048, 049 |

Separately filed: **drv-010** — dogfood the pyramid on this repo with a
Testcontainers integration rung (depends in spirit on drv-009).

Follow-ons (noted, not scheduled): per-rung **caching** (skip a rung whose inputs
are unchanged — a `race`/tournament cost lever); **flaky-rung** detection (a rung
whose verdict flips across re-runs is a watchdog signal, not a clean red); feeding
recurring harness faults back into the spec template (the flywheel).

---

# Deferred design (beyond v1)

Everything below is the fuller design the MVP deliberately does **not** build yet.
It is kept because the reasoning is settled and the v1 schema is forward-compatible
with it — the parser should tolerate (or explicitly reject-with-a-note) the extra
keys so they can be added without a schema break.

## The fuller vocabulary: `up` vs `run` vs `healthcheck`

> **Realised by drv-012** — see *"Environments"* above. The control-outcome
> semantics this section settled are the valuable, surviving part; drv-012 only
> reorganises the three commands out of per-rung inline keys into **named,
> reusable environments** a level references by name. The reverse-`wants:`
> "pass the ball" wiring (next section) remains deferred.

Beyond the single `run` command, a rung could carry three, each mapping a distinct
failure to a distinct control outcome — because a red suite means three completely
different things:

| Command | Purpose | Non-zero exit means | Control outcome |
| --- | --- | --- | --- |
| `up` / `down` | Provision & tear down an **ephemeral env draiver owns** (a DB container, a stub server). `down` runs unconditionally, pass or fail. | The **harness** is broken — draiver could not even set up to test. | Infrastructure fault → **escalate** (this is not the agent's code failing). |
| `run` | Execute the rung's tests against the prepared env. | **The code is wrong** at this rung. | The ceiling is *below* this rung. Climbing stops; a concrete, spine-recorded failure to fix. |
| `healthcheck` | Probe a **dependent env draiver does NOT own** (a sibling's deployed infra, a staging URL). Runs *before* `run`; a red probe skips `run` entirely. | The **dependency isn't ready** — not a code fault. | **Block**, not failure → the pass-the-ball path (below). |

`up`/`down` are draiver's `ExecStartPre`/`ExecStopPost` for a rung; `healthcheck`
is closest to systemd's readiness probe pointed at *someone else's* unit. The
`up`↔`healthcheck` split is the same own-it-vs-observe-it line as the forge
coupling's "local branch we land" vs "remote ref we only fetch and verify."

*v1 folds env bring-up/teardown into the rung's single `run` command; splitting
`up`/`down` out (for guaranteed teardown-on-failure) is the first natural
increment.*

## The `healthcheck` rung *is* the coordinator's "pass the ball" trigger

`capabilities-and-supervision.md` describes an integration/e2e ticket that
*"discovers infra isn't delivering X"* and escalates up the `wants:` edge to wake
the Capability coordinator. That doc left the discovery mechanism abstract. The
test pyramid makes it **concrete and generic**:

- The e2e ticket's top rung carries a `healthcheck` that probes the sibling
  `INFRA-1`'s deployed environment.
- A **red `run`** → the e2e ticket's *own* code is wrong → fix it locally.
- A **red `healthcheck`** → the sibling hasn't delivered → **escalate-and-recommend
  up `wants:`** (reverse-`wants:` activation, drvctl-041).

So the pyramid does not just coexist with the Capability design — it **completes**
it: the `healthcheck`-vs-`run` distinction is the machine-readable signal that tells
"pass the ball" (human-gated cross-ticket contract) apart from "fix your code" —
without the agent or coordinator having to *judge* which it is. *Deferred until a
coordinator fleet needs it; it would be its own ticket wiring a red `healthcheck`
into the reverse-`wants:` path.*

## How high, the fuller model: capability ceiling vs required floor vs default

v1 uses the file's top rung as the single bar. The fuller model is three numbers in
three stores (per `targets-and-dependencies.md` §3):

1. **Capability ceiling — how high this repo/attempt *can* reach. Derived, stored
   nowhere.** The highest rung whose `up` succeeds, `healthcheck` (if any) is green,
   and `run` passes. A code repo with no staging access *cannot* clear an e2e rung
   whose healthcheck fails — a fact computed at run time, never stored.
2. **Required floor — how high a ticket *must* reach before Review. Design input →
   `spec.md` frontmatter** (`test_level: integration`), the `depends.go` authoring
   path. *Deferred: v1 lets the file's top rung be the bar.*
3. **Global / project default — the floor when a ticket names none. Config**
   (`default_test_level`), the `default_supervision` precedent. *Deferred with the
   floor.*

## systemd mapping (extends `draiverctl.md`'s analogy table)

| systemd | draiver test pyramid |
| --- | --- |
| Exit 0 as ground truth | a rung's `run` exit code — the spine's trustworthy verdict *(v1)* |
| `ExecStartPre=` / `ExecStopPost=` | a rung's `up` / `down` *(deferred)* |
| Readiness probe against a dependency | a rung's `healthcheck` *(deferred)* |
| `ExecStopPost` "run tests, open PR on review" (reserved) | the daemon's retire step runs `draiver test` on retire *(v1 is agent-driven via `--log`; the daemon-run path is a later increment)* |

## Resolved forks (kept for the record)

1. **Who triggers the authoritative run?** *Resolved: `draiver test` is the sole
   executor and sole writer; the agent invokes it (v1). A daemon-on-retire path is a
   later increment.* Because draiver interprets the exit code, the caller cannot
   fake the verdict — so adding the daemon path later costs no trust.
2. **Is a missing `.test-pyramid.yaml` an error?** *Resolved: no — it is a valid
   non-error state (no file → no gate),* mirroring config's "missing file →
   defaults." (An escalate-when-a-floor-is-set-but-no-file case only arises once the
   deferred floor exists.)
3. **Rung-name vocabulary — enum or free strings?** *Resolved: free, ordered
   strings.* The pyramid is the *list order*, not a fixed enum; a data pipeline's
   pyramid is not a web app's.
4. **Env isolation & secrets for `up`/`healthcheck`.** Still open, and it is why a
   bare code repo *should* top out below e2e: don't hand staging creds to every
   worktree; the coordinator/e2e ticket that owns the creds clears the top. Relevant
   only once `up`/`healthcheck` land.
5. **Timeouts.** A hung `run` must not wedge anything: a per-rung `timeout:` in the
   yaml, a trip recorded as a fault. A cheap early add when needed.

## BDD artefact capture & storage (drv-017)

A BDD/acceptance rung produces **evidence** — cucumber-JSON, embeddings
(screenshots), and script-output files. These are **reproducible, not
version-controlled**: they belong in the data root, not the code repo. drv-017 adds
the capture-and-storage layer that puts them there, provenance-anchored.

- **What is captured.** The rung's **cucumber-JSON** report — the first-class
  `cucumber_json:` field drv-016 already consumes live — plus any additional outputs
  it declares in an `artifacts:` list (embeddings/screenshots dir, script-output
  files), all worktree-relative. A rung is a capture rung when it declares *either*,
  so a BDD rung that only sets `cucumber_json:` still has its report stored:

  ```yaml
  - name: bdd
    environment: local
    run: sh run-acceptance.sh        # emits report.json + shots/ (the crystallised steps)
    cucumber_json: report.json       # the report (drv-016) — captured as the lead artefact
    artifacts:
      - shots                        # extra evidence; a dir is captured recursively
  ```

  These outputs should be **git-ignored** — they are regenerated evidence, so
  keeping them untracked lets repeated `draiver test --log` runs stay clean and not
  trip the dirty-tree guard.

- **Per-run key.** On a green `--log` run, each captured artefact is copied into the
  attempt's `artefacts/` store under
  `bdd/<rung>/<env>/<commit>/<runstamp>/…`. The key is **never clobbered**: a
  collision appends a numeric suffix, so **multiple regenerations of the same
  artefact coexist side by side** and a prior run is always recoverable.

- **Provenance.** The captured set (plus a `run.json` recording rung, environment,
  commit, runstamp, and — once drv-012 lands — the healthcheck verdict) is
  referenced from the run's `test-result` event (`artefacts:`), so it is covered by
  `draiver audit` and surfaced by `brief`.

- **Retention.** Additive by default — nothing is deleted. The operator config knob
  `bdd_artefact_keep` caps how many run sets are kept per `(rung, environment)`
  group; `0` (the default) keeps everything.

The capture is runner-agnostic (it stores whatever files the rung declares) and
decoupled from how the run is executed (drv-016) or rendered (drv-018).

## Where it lands relative to draiver's thesis

- **Success is ratified, never self-certified.** The pyramid turns *"all tests
  green"* from agent prose into a spine-established fact — the missing structural
  ratifier the platform doc already names.
- **Deterministic spine, stochastic leaves.** The repo declares (loose coupling);
  the spine executes and adjudicates (trustworthy). Non-determinism never touches
  the verdict.
- **Externalize context; the worker is disposable.** A verification result is a
  durable log event a fresh `brief` replays — the next agent inherits "integration
  green @ abc123," not a lost terminal buffer.
- **Manage by exception.** A green climb needs zero human attention; only a below-
  target claim reaches a person, pre-diagnosed by which rung failed.

The concept is not new ground: `draiverctl.md` already reserved the `ExecStopPost`
"run tests" slot and `coding-agent-platform.md` already named tests as a first-class
ratifier. The v1 design work is the three decisions — **the file's top rung is the
bar**, **`--log` gates on a clean tree and only green is logged**, and **draiver
owns the verdict while the agent drives** — none of which fight the model, and all
of which reuse machinery (`depends.go` frontmatter authoring, the permission/merge
gates) that already exists.
