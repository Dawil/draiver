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
| `--limit N` | Codebase only: at most N rows, after `--sort`. |

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
older records then read as `stale`) and `computed_at`. Token and cost figures are **not**
recomputed: they are reused from the attempt.md `metrics:` block (or the live session
meter) and shown under `usage`.

## Provenance

A metric is only useful if you know which code and which agent produced it. Each row
reports, each as `{value, source}`:

| Field | Recorded when | Fallback |
|---|---|---|
| `base_commit` | The supervisor freshly cuts the attempt's worktree (attempt.md `base_commit`). | `inferred`: merge-base of `draiver/<ticket>/<attempt>` and its base (merged branches handled), when the branch still exists; else `unknown`. |
| `repo_id` | Same moment (attempt.md `repo_id`). | `inferred` from the attempt's `repo` path; else `unknown`. |
| `model` | The model seen in the stream, folded into attempt.md `model_used` on retire. | `observed` from the transcript; else the requested `model`, labelled `requested`; else `unknown`. |
| `draiver_version` | At cut (attempt.md `draiver_version`). | `unknown`. |

`base_commit` is the key that lets you say "after this AGENTS.md change, the metric moved."

## v1 metric definitions (`metrics_version: 1`)

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
| `wall_time_s`, `tool_calls`, `calls_by_class` | First to last transcript event; calls in total and per class. | Size of the effort. |
| `diff_files`, `diff_lines` | Final diff size. | Difficulty proxy, to normalize the rest. |

### Exploration and context

| Metric | Definition | Signals |
|---|---|---|
| `exploration_prefix_calls` | Read-class and subagent calls before the **first productive edit** (first edit to a file in the final diff). `prefix_basis` says which anchor was used: `first-productive-edit`, `first-edit` (fallback when the diff is unavailable or never edited), or `no-edit`. | How much reading it took before the agent could act. The headline accessibility metric. |
| `exploration_prefix_tokens` | Context size (prompt tokens of the request) at that same point. | Context spent getting oriented. |
| `search_prefix_calls` | Search-class calls before the same anchor. | How hard the right code was to find. |
| `empty_searches` | Searches that returned nothing (incl. `grep` exit 1). | Guessing at names that don't exist. |
| `repeated_searches` | Searches repeating an earlier query, after collapsing whitespace, quotes and case. | Searching in circles. |
| `files_read_distinct` | Distinct files read. | — |
| `files_read_unused` | Files read but neither edited nor in the final diff. | Reading that didn't pay off. |
| `wasted_context_ratio` | `files_read_unused / files_read_distinct`. | Poor locality or poor signposting. |
| `rereads` | Reads of a file already read. | Context lost or not trusted. |
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

## JSON schema

`--json` on one attempt:

```json
{
  "target": {"ticket": "drv-901", "attempt": "0001"},
  "mode": "compute",
  "metrics_version": 1,
  "row": { ...Row }
}
```

`--json` on a codebase:

```json
{
  "target": {"toplevel": "/src/widget", "repo_id": "github.com/acme/widget"},
  "mode": "stored", "metrics_version": 1,
  "filters": {"since": "...", "sort": "...", "limit": 5, "include_running": false},
  "total": 12, "excluded_running": 1,
  "aggregate": {
    "groups": [{"model": "...", "count": 8, "with_metrics": 8,
                "outcomes": {"ratified": 6, "in-review": 2},
                "metrics": {"exploration_prefix_calls": {"n": 8, "median": 4, "p90": 11}, ...}}],
    "unread_docs": ["docs/guide.md"]
  },
  "rows": [ ...Row ]
}
```

A `Row` is `{ticket, attempt, source, partial, metrics_version, computed_at,
transcript_cmd, provenance, usage, metrics}`, where `metrics` holds every field named in
the tables above (with `prefix_basis`, `final_diff` and `transcript`), and is `null` when
`source` is `missing` or `stale` under `--stored`. `total` counts rows before `--limit`.
Aggregates are grouped by model because model changes swamp codebase changes.

## Out of scope (v1)

A/B or before/after comparison (do it from the JSON), `--follow`/streaming, exporters or
dashboards, the web UI, and recommending evals.
