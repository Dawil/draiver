# `draiver metrics` — agent-performance metrics (drv-054)

`draiver metrics` measures how **accessible a codebase is to an agent**: how much an
agent had to explore, search, guess and ask before it could act. The numbers are
folded from what agents actually did — each attempt's recorded session transcript
(`attempts/<id>/session/stream.jsonl`), its log, and its final diff — and can be read
per attempt or across every attempt on a repository.

They exist to answer one question: **which evals are worth writing?** A codebase fact
an agent keeps rediscovering belongs in the codebase (AGENTS.md, a script, a test, a
linter); a regression in it is caught by an eval. The metrics show where agents stumble,
so you know where to look.

## Usage

```
draiver metrics TICKET [--attempt A]        # one attempt (default: the latest)
draiver metrics --codebase PATH             # every attempt on PATH's repository
```

| Flag | Meaning |
|---|---|
| `--stored` (default) | Return only persisted values. **Never computes.** Each row's `source` is `stored`, `missing` (never computed) or `stale` (computed under an older `metrics_version`). |
| `--compute` | Compute attempts that are `missing` or `stale`, persist them, return everything. Recomputed rows have `source: computed`. |
| `--recompute` | As `--compute`, but recompute every selected attempt. |
| `--json` | Emit the stable JSON schema below. |
| `--include-running` | Include running attempts. Their rows are `partial: true` and are **never persisted**. |
| `--since DATE` | Codebase only: attempts started on or after `YYYY-MM-DD` / RFC3339. |
| `--sort METRIC` | Codebase only: rows worst-first (descending) by a numeric metric; rows without a value sort last. |
| `--limit N` | Codebase only: at most N rows, after `--sort` (or N hotspots with `--by`). |
| `--by file\|command` | Codebase only: a hotspot view instead of attempt rows (see [Hotspot views](#hotspot-views)). Not combinable with `--sort`. |
| `--split-at COMMIT` | Codebase only: report the aggregate per cohort — attempts whose `base_commit` contains COMMIT (**after**) and those whose base does not (**before**). Attempts with an unknown base are counted and excluded. Works with `--by` and `--json` (see [Before/after a change](#beforeafter-a-change---split-at)). |

"The worst 5 attempts by exploration prefix":

```
draiver metrics --codebase . --compute --sort exploration_prefix_calls --limit 5
```

Every row carries `transcript_cmd` — `draiver ctl logs TICKET --attempt A` — so you can
drill from a number to what the agent actually did.

### Selecting a codebase

`--codebase PATH` resolves PATH to its git toplevel and its `repo_id` (the normalized
`origin` URL, or the canonical toplevel when there is no `origin`). An attempt matches on
its recorded `repo_id`; a legacy attempt without one matches on its `repo` path's
toplevel, or on the `repo_id` inferred from that path — so two local clones of the same
remote are one codebase. This is about a *repository*; it is unrelated to `type: codebase`
ledger tickets, which are never selected.

### Storage

Computed metrics are persisted per attempt in `attempts/<id>/metrics.json`: derived,
outside the hash-chained log, safe to delete (the next `--compute` rebuilds it from the
transcript). Each record carries `metrics_version` (bumped whenever a definition changes —
older records then read as `stale`) and `computed_at`. Token figures are **not**
recomputed: they are reused from the attempt.md `metrics:` block (or the live session
meter) and shown under `usage`. There is no dollar cost: the meter's figure was
unreliable, and no verified per-model price table exists to derive one deterministically.
Cost can be derived from `usage` outside draiver.

### Determinism

Every value is a pure fold of the recorded transcript, the hash-chained log and git. There
is no LLM, no model judgement, and no clock read at compute time (`computed_at` aside).
The same inputs at the same `metrics_version` give byte-identical JSON. A unit test
(`TestComputeIsDeterministic`) and a `@drv-054` scenario both enforce this.

A `stale` row under `--stored` still shows its old values, labelled `stale`. Codebase
aggregates, inventions, unread docs and `--by` views use **current-version rows only**,
so definitions never mix in one median. The skipped rows are counted in `excluded_stale`.

## Provenance

A metric is only useful if you know which code and which agent produced it. Each row
reports, each as `{value, source}`:

| Field | Recorded when | Fallback |
|---|---|---|
| `base_commit` | The supervisor freshly cuts the attempt's worktree (attempt.md `base_commit`). | `inferred`: merge-base of `draiver/<ticket>/<attempt>` and its base, when the branch still exists; else `unknown`. Merged branches are handled. A tip that is a landing merge (`tip^1` on the base branch's first-parent history) uses `tip^2` as the head. A tip that merged main **into** the branch is not a landing merge, so the ancestry path is used instead. |
| `repo_id` | Same moment (attempt.md `repo_id`). | `inferred` from the attempt's `repo` path; else `unknown`. |
| `model` | The model seen in the stream, folded into attempt.md `model_used` on retire. | `observed` from the transcript; else the requested `model`, labelled `requested`; else `unknown`. |
| `draiver_version` | At cut (attempt.md `draiver_version`). | `unknown`. |

`base_commit` is the key that lets you say "after this AGENTS.md change, the metric moved."
`--split-at` does exactly that (below).

## Before/after a change (`--split-at`)

```
draiver metrics --codebase . --split-at 3f2c1ab            # the AGENTS.md edit
draiver metrics --codebase . --split-at 3f2c1ab --by file --json
```

`--split-at COMMIT` resolves COMMIT in the codebase's repo (an unknown commit is an error,
so a typo can't put every attempt in "before") and sorts each selected attempt by its
`base_commit` provenance, recorded or inferred:

- **after** — the base contains COMMIT (COMMIT is the base or an ancestor of it): the
  attempt was cut from code that already had the change.
- **before** — the base does not contain COMMIT.
- **unknown** — no `base_commit`, or one the repo doesn't have (a pruned or foreign
  commit). These are counted in `excluded_unknown_base`, listed in `unknown_base`, and
  left out of both cohorts — never guessed into one.

Each cohort gets the full aggregate (per-model groups, unread docs, inventions,
`excluded_stale`) — or, with `--by`, the full ranking. It is a clean before/after for one
change without date windows, so unrelated attempts running concurrently on an older base
land in the right cohort. The other filters (`--since`, `--include-running`) select first;
cohorts split what they selected. Model groups still apply inside each cohort: compare
like with like.

## Metric definitions (`metrics_version: 3`)

v1 was the first release. v2 (drv-054 review #19) added sessions, active time, context
figures, compactions, the reread split, invention detection, structural round-trip
signals and the `--by` views. It also fixed base inference for main-merged-into-branch,
counted cobra `unknown command` output as env discovery, and dropped `cost_usd`. v3
(drv-057) follows `cd` when resolving Bash paths (see [Paths](#paths)), so scratch work in
`/tmp` no longer shows up as phantom repo files in reads, edits, `transient_files` and
`reverted_edits`. v2 rows read as `stale`; `--compute` refreshes them.

Every transcript metric is an incremental fold over normalized `agent.Event`s
(`internal/metrics`: `State`, `Step`, `Finalize`). Claude's wire format is never parsed
directly, and repeated per-line `usage` is deduplicated by message id.

### Tool-call classes

Every tool call is classified as `read`, `search`, `edit`, `build`, `test`, `vcs`,
`subagent` or `other` by one table-driven classifier (`internal/metrics/classify.go`).
Bash is classified by its command: `cat`/`head`/`tail`/`sed -n`/`less` → read;
`grep`/`rg`/`find`/`ls`/`fd` → search; `go test`/`npm test`/`pytest`/`make test`/
`draiver test`/… → test; `go build`/`make`/`npm run build`/… → build; `git …` → vcs.
Bash that writes files (`sed -i`, `>`/`>>` redirects, `tee`, heredocs, interpreter
scripts that open files for writing) contributes **edits** independently of its class,
because real agents edit mostly through Bash. Task/Agent calls are `subagent` and count as
exploration.

### Paths

Only files inside the attempt's worktree count. An absolute path is made relative to the
session's working directory; one outside it (`/tmp`, `~/.claude`, the data root) is
dropped. Within one Bash command, a `cd DIR` (or `pushd`) segment — `cd DIR &&`,
`cd DIR;` or `cd DIR` on its own line — moves where the later segments' relative paths
resolve: `cd /tmp/x && cat > a.md` writes `/tmp/x/a.md` (dropped), `cd sub && cat > a.md`
writes `sub/a.md`. Relative paths that climb out of the worktree (`cd .. && touch a`) are
dropped. A `cd` whose target can't be known statically (`cd`, `cd ~`, `cd -`, `cd $DIR`,
`popd`) makes the directory unknown, and relative paths after it are dropped rather than
guessed, until a `cd` to an absolute path. The `cd` is tracked within one command only: a
working directory the harness carries from one Bash call to the next is not followed.

### Final diff

The files changed on the attempt branch relative to `base_commit`. When it can't be
determined (no branch, no base) `final_diff` is `unavailable` and each metric below falls
back as described.

### Outcome and totals

| Metric | Definition | Signals |
|---|---|---|
| `outcome` | `ratified` (Done), `in-review`, `rejected` (a review claim followed by more work or abandonment), `escalated-unresolved`, `abandoned`, `running`. From the log and state, never the transcript. | Whether the work landed. A field, not a filter. |
| `review_round_trips` | Review claims made. | >1: the first claim wasn't what the human wanted — spec or verification gaps. |
| `escalations` (+ `escalation_texts` in JSON) | Escalations raised. | What the agent couldn't decide from the codebase. |
| `human_interrupts` | Interrupts and messages to the session. | Humans steering mid-flight. |
| `round_trips` | Per review claim: `review_seq`, `next_review_seq`, `human_events` (human-authored `decision`/`note` events after the claim, up to the next one), `human_superseded` (those later superseded, i.e. retracted), `spec_changed` (always null: spec edits are not on the log). Structural only. Prose is never classified. | Where reviews bounce, and whether the feedback was stable. |
| `wall_time_s`, `tool_calls`, `calls_by_class` | First to last transcript event (includes parked and idle time); calls in total and per class. | Size of the effort. |
| `active_time_s` | Sum of segment spans (first to last event of each segment). | Time actually worked, without parks. |
| `diff_files`, `diff_lines` | Final diff size. | Difficulty proxy, to normalize the rest. |

### Sessions and context

`stream.jsonl` is append-only across an attempt's sessions, and every (re)start emits a
system frame carrying a `SessionID`. A **session** is a distinct `SessionID`: a fresh
conversation, cold-started from the brief. A **segment** is each system frame within a
session: a resume after a park or a resolved escalation. Context is the main
conversation's prompt tokens per request. Usage is deduped by message id, and subagent
requests (`ParentToolID` set) are excluded.

| Metric | Definition | Signals |
|---|---|---|
| `session_count`, `segments` | Distinct SessionIDs; system frames. | Restarts. Many sessions means context ran out or was flushed. |
| `sessions[]` | Per session: `id`, `started`, `ended`, `segments`, `calls`, `start_context`, `peak_context`, `context_at_first_productive_edit`, `ended_by`. | Where each conversation spent its window. |
| `sessions[].ended_by` | **From the log, never the transcript.** It is `context-auto-stop` when an escalation tagged `context-auto-stop` falls at the session's end. Otherwise it is the latest of `review` / `escalation` / `park` / `done` there, or `none`. "At the end" means timestamped in `[last event − 10 min, next session start)` (`SessionEndSlack`). | Why the conversation stopped. |
| `context_auto_stops` | Log escalations tagged `context-auto-stop`. | Sessions that ran out of context. The strongest "too much to read" signal. |
| `context_floor` | `start_context` of the first session. | Fixed cost of the brief, the system prompt and AGENTS.md before any work. |
| `compactions` | Within a session, a request whose prompt is ≥ 10,000 tokens smaller than the previous one **and** under half of it (`CompactionMinDrop`, `CompactionRatio`). | The harness compacted the history. |

### Exploration and context

| Metric | Definition | Signals |
|---|---|---|
| `exploration_prefix_calls` | Read-class and subagent calls before the **first productive edit** (first edit to a file in the final diff). `prefix_basis` says which anchor was used: `first-productive-edit`, `first-edit` (fallback when the diff is unavailable or never edited), or `no-edit`. | How much reading it took before the agent could act. The headline accessibility metric. |
| `exploration_prefix_tokens` | Context size (prompt tokens of the request) at that same point. | Context spent getting oriented. |
| `exploration_prefix_tokens_net` | `exploration_prefix_tokens` minus the `start_context` of the session the anchor edit is in. | Orientation cost net of the fixed floor. |
| `search_prefix_calls` | Search-class calls before the same anchor. | How hard the right code was to find. |
| `empty_searches` | Searches that returned nothing (incl. `grep` exit 1). | Guessing at names that don't exist. |
| `repeated_searches` | Searches repeating an earlier query, after collapsing whitespace, quotes and case. | Searching in circles. |
| `files_read_distinct` | Distinct files read. | — |
| `files_read_unused` | Files read but neither edited nor in the final diff. | Reading that didn't pay off. |
| `wasted_context_ratio` | `files_read_unused / files_read_distinct`. | Poor locality or poor signposting. |
| `rereads` | Reads of a file already read. Split into the three below, which sum to it. | — |
| `rereads_cross_session` | The first read in a session of a file read in an earlier one. | Forced by restarts, not by the agent. |
| `rereads_same_range` | A read of a slice (or the whole file) already read in this session. A ranged read is Read `offset`/`limit`, `sed -n`, `head`, `tail` or `awk`. | True rereads: context lost or not trusted. |
| `ranged_reads` | A new slice of a file already read in this session. | Normal for big files, but many of them point at an oversized file. |
| `docs_read` | Paths read that are `AGENTS.md`, `CLAUDE.md`, `README*`, or under `docs/`. | Whether the docs are found at all. |
| `first_doc_read_index` | Tool-call index of the first doc read (null if none). | Whether docs are the first stop or a last resort. |
| `unread_docs` (codebase only) | Doc files tracked at the repo's HEAD that no selected attempt read. | Candidates for undiscoverable or dead docs. |

### Errors and discovery

Failed tool calls are split by kind — never one blended rate.

| Metric | Definition | Signals |
|---|---|---|
| `env_discovery_errors` | Command not found / exit 127, wrong invocation or usage errors, a missing script, target or binary, missing env or auth. | **The AI-accessibility signal**: the agent couldn't find or drive the environment. |
| `test_failures` | Red test runs. | Normal TDD; kept apart so it doesn't pollute the above. |
| `build_failures` | Failed builds that aren't env-discovery errors. | Compile/iteration churn. |
| `edit_mechanics_errors` | Malformed tool use (Edit `old_string` mismatch, stale read, input validation). | Nothing about the codebase. |
| `permission_denials` | draiver's gates or the harness refusing a call. | Nothing about the codebase. |
| `other_errors` | Any other failed call. | — |
| `tries_to_first_successful_test` / `_build` | Test (build) runs up to and including the first green one; null if never green. | How hard it is to get the test/build loop working. |
| `tokens_to_first_green_test` | Cumulative tokens (input + cache read/write + output, per message) up to the first green test. | Cost of reaching a working loop. |
| `time_to_first_green_test_s` | Seconds from the first event to the first green test. | — |
| `reverted_edits` | Files edited during the attempt that are absent from the final diff (null when the diff is unavailable). | False starts. |

### Invention

| Metric | Definition | Signals |
|---|---|---|
| `transient_files` | Files written during the attempt (Write, redirect, heredoc, `tee`, …) that were **not tracked at `base_commit`** and are **absent from the final diff**: created, then thrown away. Null when the diff is unavailable. | Scratch the agent needed and the codebase didn't provide. |
| `ticket_scoped_test_files` | Test files in the final diff whose base name carries the attempt's own ticket id **and** a throwaway prefix (`zz_`, `tmp_`, `adhoc`), e.g. `zz_drv044_focus_test.go`. | A focus harness built because the suite can't run a subset. |
| `inventions` (codebase only) | Both of the above, grouped by name pattern (ticket ids and the text between the throwaway prefix and the test suffix collapse to `*`, e.g. `features/acceptance/zz_*_test.go`), most attempts first. | The same workaround reinvented across tickets. Build it once, in the codebase. |

## Hotspot views

`draiver metrics --codebase PATH --by file|command` ranks across the selected attempts
instead of listing rows. It aggregates current-version rows only.

**`--by file`** is ranked by **context tokens, not calls**. A whole-file Read of a large
file is one call, but it can cost 15k tokens. Per file:

| Field | Definition |
|---|---|
| `context_tokens` | Main-context growth attributed to the file's read results. Each request's prompt-token delta over the previous request is split across the read results that arrived in between, in proportion to their characters. |
| `result_chars` | Total characters of the file's read results. |
| `attempts`, `reads`, `whole_reads` | Attempts that read it; reads; reads without a range. |
| `unused_in` | Attempts that read it but neither edited it nor shipped it. |
| `cross_ticket_readers` | Attempts that read it while its name carries another ticket's id (`drv-046`, `drv046`, `drv_046`; same prefix family, so `drv` ~ `drvweb` ~ `drvctl`). For example, `steps_boardcontrols_drv046.go` read by drv-048. |
| `lines_at_head` | Line count at the codebase's HEAD (null when untracked or binary). |

Signals: files to split, and code borrowed from another ticket that belongs in a shared place.

**`--by command`** lists normalized Bash commands and searches that recur in at least
two attempts. Normalization drops a leading `cd DIR &&`, `2>&1`-style duplications
and a trailing `| head`/`| tail -N`. It collapses whitespace, quotes and case, and maps
`drv-044`-style ticket ids to `<ticket>` and digit runs to `N`. Signals: a ritual every
agent rediscovers, which a script or an AGENTS.md line could replace.

## JSON schema

`--json` on one attempt:

```json
{
  "target": {"ticket": "drv-901", "attempt": "0001"},
  "mode": "compute",
  "metrics_version": 3,
  "row": { ...Row }
}
```

`--json` on a codebase:

```json
{
  "target": {"toplevel": "/src/widget", "repo_id": "github.com/acme/widget"},
  "mode": "stored", "metrics_version": 3,
  "filters": {"since": "...", "sort": "...", "limit": 5, "split_at": "...", "include_running": false},
  "total": 12, "excluded_running": 1,
  "aggregate": {
    "groups": [{"model": "...", "count": 8, "with_metrics": 8,
                "outcomes": {"ratified": 6, "in-review": 2},
                "metrics": {"exploration_prefix_calls": {"n": 8, "median": 4, "p90": 11}, ...}}],
    "unread_docs": ["docs/guide.md"],
    "excluded_stale": 0,
    "inventions": [{"kind": "ticket-scoped-test", "pattern": "features/acceptance/zz_*_test.go",
                    "attempts": ["drv-044@0001", "drv-047@0001"], "files": ["..."]}]
  },
  "split": { ...only with --split-at, below },
  "rows": [ ...Row ]
}
```

With `--split-at`, `split` carries the aggregate per cohort; `aggregate` stays the
whole selection's:

```json
"split": {
  "commit": "3f2c1ab", "sha": "3f2c1ab9e0...",
  "excluded_unknown_base": 1, "unknown_base": ["drv-003@0001"],
  "before": {"attempts": 7, "refs": ["drv-040@0001", "..."], "aggregate": { ...as above }},
  "after":  {"attempts": 4, "refs": ["drv-051@0001", "..."], "aggregate": { ...as above }}
}
```

A `Row` is `{ticket, attempt, source, partial, metrics_version, computed_at,
transcript_cmd, provenance, usage, metrics}`, where `metrics` holds every field named in
the tables above (with `prefix_basis`, `final_diff` and `transcript`, plus `files[]` and
`commands[]`, the per-attempt inputs to the `--by` views). It is `null` when `source` is
`missing`. A `stale` row keeps its older values. `total` counts rows before `--limit`.
Aggregates are grouped by model because model changes swamp codebase changes.

`--by file|command --json`:

```json
{
  "target": {...}, "mode": "stored", "metrics_version": 3, "by": "file",
  "filters": {...}, "attempts": 12, "with_metrics": 11, "excluded_stale": 1,
  "files": [{"path": "internal/web/web.go", "attempts": 5, "reads": 44, "whole_reads": 1,
             "result_chars": 181402, "context_tokens": 93547, "unused_in": 0,
             "cross_ticket_readers": [], "lines_at_head": 2970}],
  "commands": [{"command": "go test ./internal/web/...", "attempts": 3, "count": 7, "in": ["drv-044@0001", "..."]}]
}
```

Only one of `files` and `commands` is present, matching `by` (and omitted when empty).
With `--split-at`, a `split` object carries the same ranking per cohort:
`{commit, sha, excluded_unknown_base, unknown_base, before: {refs, attempts, with_metrics,
excluded_stale, files|commands}, after: {...}}`.

## Out of scope

Statistical A/B testing (`--split-at` gives the cohorts; significance is yours to judge — or
use the controlled eval suite), `--follow`/streaming, exporters or
dashboards, the web UI, and recommending evals. Also a replay-eval harness, which would
re-run a historical ticket at its `base_commit` with a candidate change. Its agent runs
are non-deterministic, so it is its own ticket, with `draiver metrics` as its
deterministic scorer.
