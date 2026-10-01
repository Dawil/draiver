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
green" is a phrase the agent *types into its review claim* (`handbook.md` §5:
*"all tests green; covers the spec's three acceptance criteria"*). That is exactly
the self-report the platform's trust model says it structurally **cannot** trust
(`coding-agent-platform.md`: *"Success is a claim, not exit 0 … the platform's
trust model cannot extend to the worker's self-report"*).

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

**The Review gate.** `draiver review` folds the log for the highest logged rung at
current HEAD; if a `.test-pyramid.yaml` exists but no result covers HEAD at the
target rung, the claim is **refused** with a message naming the gap and the fix
(`draiver test --log`). No file → no gate (not a failure). This is the
enforce-by-process-control seam, the same shape as the permission gate and
`merge`'s clean-checkout gate. It does not remove the human Review gate — it
shrinks the judgment surface behind it. `brief` surfaces the result for free (it is
a replayed log event), so a fresh agent inherits "integration green @ abc123."

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

- **Declaring outputs.** A rung declares the files/dirs it produces with an
  `artifacts:` list (worktree-relative), optionally naming the `environment:` it
  binds to:

  ```yaml
  - name: bdd
    environment: local
    run: sh run-acceptance.sh        # emits cucumber.json + shots/ (the crystallised steps)
    artifacts:
      - cucumber.json
      - shots                        # a dir is captured recursively
  ```

  The declared outputs should be **git-ignored** — they are regenerated evidence,
  so keeping them untracked lets repeated `draiver test --log` runs stay clean and
  not trip the dirty-tree guard.

- **Per-run key.** On a green `--log` run, each declared artefact is copied into the
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
