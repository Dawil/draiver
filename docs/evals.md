# Agent-context evals (drv-065)

The `evals` rung tests this repo's **agent context** at a commit. That means
`AGENTS.md`, the docs, the READMEs and how easy the tests are to drive. It runs a
headless agent (`claude -p`) against a throwaway clone of the repo and scores what it
does. **No draiver attempts are run** and nothing is replayed. That boundary was
drawn in drv-054.

## A repo-owned pyramid level, not a draiver feature

draiver has no eval framework and no eval-specific interface. The rung uses only the
pyramid contract every level already has ([`test-pyramid.md`](./test-pyramid.md)):

| Contract | In `.test-pyramid.yaml` |
|---|---|
| `run:` | `sh evals/run.sh` |
| `environment:` | `agent`. `up` installs the pinned tooling. The healthchecks require `claude` and credentials, so a box without them **blocks** the rung rather than passing it. |
| `required: false` | Advisory and at the top of the ladder, so a bare `draiver test` never runs it and it never gates review. |
| `artifacts:` | `evals/_artifacts`, which holds `report.json`, `report.html` and `summary.md`. |
| `--only` | `draiver test evals --only` runs just this rung, captures the report and records **no** gate result. |

The pass-rate **threshold lives in the repo**, in `evals/promptfooconfig.yaml`
(`PROMPTFOO_PASS_RATE_THRESHOLD`, currently 80%). The rung exits non-zero below it.
Draiver's own suite is one way to meet this contract. Another repo can use any tool.

## The suite

[promptfoo](https://www.promptfoo.dev/), pinned to exactly `0.121.20` in
`evals/package.json`. That is the last release line that runs on Node 20. It was
chosen over plain `go test -tags=evals` because it brings repeats, pass-rate
thresholds, JSON/HTML reports and LLM-rubric grading, which we would otherwise build
ourselves. The cost is size: `evals/node_modules` is about 1.8 GB, and a cold install
takes about 5 minutes. Never copy `evals/` around.

```
evals/
  promptfooconfig.yaml   threshold, repeats (default 5), concurrency, grader
  cases/knowledge.yaml   question → answer checks
  cases/tasks.yaml       E1, E3
  lib/provider.cjs       one case: sandbox → setup → claude -p → checks → teardown
  lib/sandbox.cjs        throwaway clone + isolated env + tripwire
  lib/tasks.cjs          task setups and deterministic checks
  lib/grader.cjs         tool-less claude -p grader for llm-rubric
  run.sh                 the rung's run:, writes evals/_artifacts/
```

**Knowledge checks** ask a question the docs should answer, such as how to climb
without blocking or what every BDD scenario must attach. They grade the answer with
deterministic assertions (`icontains-all`, `regex`). An `llm-rubric`, graded by
`claude -p --model haiku` with no tools, is added only where the rule is a judgement,
such as "does not recommend sleep-polling". The agent gets read-only tools.

**Task checks** do a small task, then check the checkout deterministically:

- **E1, add a BDD scenario for a small web change.** Setup commits a "Ledgers →" link
  in the board's top bar. Passing requires all of these:
  - a new `.feature` tagged `@evl-001`;
  - its focused run green, through the real `acceptance.Run` with only that feature;
  - every scenario attaches an artefact;
  - the unit rung (`go test ./...`) green;
  - the web change still present;
  - no new `zz_*_test.go` left behind.
- **E3, main has moved.** Setup cuts `draiver/evl-003/0001` with two files of its own,
  then adds unrelated commits to `main` (two new files and an edit to `docs/mvp.md`).
  The agent writes a PR description. Passing requires all of these:
  - it names both branch files and none of main's;
  - the branch diff against the merge base touches only the branch's own files, so
    nothing was restored or reverted from base (untracked new files such as a
    changelog fragment are fine);
  - `main` was not moved.

Each row reports the agent's turns, tokens and cost (from Claude's JSON output), and
`summary.md` totals them per case.

### Adding or adjusting a case

Each docs ticket owns the knowledge check(s) for what it documents. Add or edit an
entry in `cases/knowledge.yaml` with `metadata: {kind: knowledge, ticket: <id>}`.
Prefer `icontains`/`regex` on `JSON.parse(output).answer`, and add an `llm-rubric`
only for judgement calls. A new task case needs a setup and a check in
`lib/tasks.cjs` and an entry in `cases/tasks.yaml`.

## Isolation

Every case gets a fresh box under `EVALS_SANDBOX_DIR` (default
`~/.cache/draiver-evals/`), and the box is removed afterwards:

- a `git clone` of this checkout at `EVALS_REF` (default `HEAD`, which is **committed**
  state only), with **every remote removed** and `main` pinned to the case's base;
- `DRAIVER_DATA`, `DRAIVER_CONFIG`, `XDG_CACHE_HOME` and `TMPDIR` inside the box.
  `GOCACHE` and `PLAYWRIGHT_BROWSERS_PATH` stay pointed at the real caches so builds
  stay warm;
- every inherited `DRAIVER_*`, `CLAUDECODE`, `CLAUDE_CODE_*` session variable and
  `PROMPTFOO_*` variable is dropped. That makes it safe to run from inside a
  supervised session: the agent can't log to the parent attempt or message the
  parent session;
- `claude -p --setting-sources project,local`, so the operator's user-level
  settings, hooks and plugins don't contaminate what is being measured;
- a **tripwire**. If the source checkout's HEAD, working-tree status or `main` changes
  during a case, the row becomes an error ("ISOLATION BREACH"), not a pass.

The box is not under `/tmp`, which is often a small tmpfs, and not inside the repo.
Inside the repo, Claude's upward search for `CLAUDE.md`/`AGENTS.md` would read the real
checkout's context. promptfoo's own state goes to a temp `PROMPTFOO_CONFIG_DIR`
(`--no-write --no-cache --no-share`).

Bash in the agent is not sandboxed at the OS level. The tripwire is what catches an
agent that `cd`s out of its clone. The tripwire can't tell who made the change, so
**don't commit or edit in the checkout while a run is in flight**: your own commit
would turn every in-flight case into an isolation error.

## Running it

```sh
draiver test evals --only                       # the whole suite, 5 repeats
EVALS_REPEAT=1 draiver test evals --only        # quick look
EVALS_FILTER=kind=knowledge draiver test evals --only
EVALS_FILTER=ticket=drv-059 draiver test evals --only
sh evals/run.sh                                 # the same, without draiver
```

Other knobs: `EVALS_MODEL` (agent model), `EVALS_GRADER_MODEL` (default `haiku`),
`EVALS_MAX_BUDGET_USD` (per-case spend cap), `EVALS_E1_UNIT_CMD` (default
`go test ./...`) and `EVALS_KEEP=1` (keep the clones for debugging).

A full run takes a long time and runs in the foreground: `--only` has no `--detach`.
From an agent session, start it as a background command and wait for the notification.

## Measuring a change

To see whether a change (usually a docs ticket) helped, hold the **questions** fixed
and vary only the **context**:

1. Check out the change's commit, with its version of `evals/`, and run
   `draiver test evals --only`. Keep `evals/_artifacts/summary.md`.
2. In the same checkout, run `EVALS_REF=<commit>^ draiver test evals --only`. The same
   suite now runs against the parent's agent context. Keep that `summary.md` too.
3. Compare pass rates per case, along with turns and tokens. Log both tables on the
   parent ticket (drv-055).

Running the suite *from* the parent's checkout also works once the parent has an
`evals/` tree, but then a ticket that adds its own knowledge check would be compared
with a different question set. Use `EVALS_FILTER=ticket=<id>` to rerun just the cases a
ticket targets. With 5 repeats, a swing of one or two passes is noise.

**Rough cost** (Claude subscription or API, measured on the drv-065 baseline): a
knowledge row costs about $0.05–0.10 and 15–30 s. An E3 row costs about $0.15 and
under a minute. An E1 row is the expensive one: an agent writing a scenario, a focused
run with Playwright and a full `go test ./...` take roughly 15–25 minutes. A full
default run (6 knowledge + 2 task cases × 5 repeats = 40 rows) is dominated by E1. See
the baseline logged on drv-055 for measured totals.
