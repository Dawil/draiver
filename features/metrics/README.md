# drv-054 metrics fixtures

`fixtures/widget.stream.jsonl` is a hand-built transcript shaped exactly like Claude
Code's recorded `session/stream.jsonl`. Regenerate it with
`python3 features/metrics/fixtures/gen_widget.py`. Every call is chosen so the right
metric values are obvious. With a final diff of just `src/widget.go` (as
`metrics.feature` seeds it), the expected v1 values are:

| # | Call | Class | Counts toward |
|---|---|---|---|
| 0 | `Read AGENTS.md` | read | exploration prefix; `docs_read`, `first_doc_read_index = 0`; unused |
| 1 | `Grep Widget` → no matches | search | search prefix; empty search |
| 2 | `grep -rn widget src` → exit 1 | search | search prefix; empty search (not an error) |
| 3 | `Grep widget` | search | search prefix; repeat of #1 (case-folded) |
| 4 | `cat src/widget.go` | read | exploration prefix |
| 5 | `Read src/unused.go` | read | exploration prefix; unused |
| 6 | `Task explore` | subagent | exploration prefix |
| 7 | `make lint` → no rule | build | env-discovery error; build try 1 |
| 8 | `widgetctl --help` → 127 | other | env-discovery error |
| 9 | `Edit` with a wrong `old_string` | edit | edit-mechanics error (no effect) |
| 10 | `Write src/scratch.go` | edit | first edit, but not productive |
| 11 | `Edit src/widget.go` | edit | **first productive edit** |
| 12 | `go build ./...` | build | build try 2, first green build |
| 13 | `go test ./...` → FAIL | test | test failure; test try 1 |
| 14 | `Read src/widget.go` | read | reread |
| 15 | `Edit src/widget.go` | edit | — |
| 16 | `rm src/scratch.go` | edit | reverted edit (`scratch.go` not in the diff) |
| 17 | `go test ./...` → ok | test | first green test (try 2, at 550s) |
| 18 | `git push --force` → gate | vcs | permission denial |
| 19 | `git commit` | vcs | — |

So: `tool_calls 20`, `exploration_prefix_calls 4`, `search_prefix_calls 3`,
`empty_searches 2`, `repeated_searches 1`, `files_read_distinct 3`,
`files_read_unused 2`, `rereads 1`, `env_discovery_errors 2`, `test_failures 1`,
`edit_mechanics_errors 1`, `permission_denials 1`, `tries_to_first_successful_test 2`,
`tries_to_first_successful_build 2`, `reverted_edits 1`. The first message's usage is
repeated on two lines (thinking + tool_use) and must be counted once.

The acceptance suite also derives a lighter variant (calls 4–6 dropped, model
`claude-fixture-2`) for a second attempt in another clone of the same remote.

## `fixtures/sessions.stream.jsonl` (metrics v2)

Regenerate with `python3 features/metrics/fixtures/gen_sessions.py`. The stream is
append-only across restarts. It has two sessions (`fixture-session-a`, `fixture-session-b`),
and the first is resumed once, so there are **3 segments**. The prompt size of every
request is set explicitly. With a final diff of `src/big.go` and
`src/zz_drv905_focus_test.go`, and `AGENTS.md`, `src/big.go` and `src/steps_drv901.go`
tracked at base:

| # | Seg | Call | Prompt | Counts toward |
|---|---|---|---|---|
| 0 | A1 | `Read AGENTS.md` | 30000 | doc; session A `start_context`, `context_floor` |
| 1 | A1 | `sed -n '1,50p' src/big.go` | 31000 | first read of big.go (ranged) |
| 2 | A1 | `sed -n '51,100p' src/big.go` | 33000 | `ranged_reads` (a new slice) |
| 3 | A1 | `sed -n '1,50p' src/big.go` | 35000 | `rereads_same_range` |
| 4 | A1 | `Read src/steps_drv901.go` | 36000 | cross-ticket read for drv-905; unused |
| 5 | A1 | `Write src/zz_drv905_focus_test.go` | 40000 | **first productive edit**; `ticket_scoped_test_files` |
| 6 | A1 | `cat > tmp/notes.txt <<EOF` | 42000 | creates `tmp/notes.txt` (not at base) |
| 7 | A1 | `cd … && go test ./src/... 2>&1 \| tail -5` | 60000 | green test; session A peak |
| — | A1 | closing text | 20000 | **compaction** (60000 → 20000) |
| 8 | A2 | `rm tmp/notes.txt` | 22000 | `transient_files` (`tmp/notes.txt`) |
| 9 | A2 | `draiver promote drv-905` → `unknown command`, not an error result | 23000 | `env_discovery_errors` |
| 10 | B1 | `Read src/big.go` (whole) | 31000 | `rereads_cross_session`; session B start |
| 11 | B1 | `Edit src/big.go` | 35000 | session B's first productive edit |
| 12 | B1 | `go test ./src/...` | 36000 | green test |
| — | B1 | closing text | 36500 | session B peak |

So: `session_count 2`, `segments 3`, `context_floor 30000`, `compactions 1`,
`exploration_prefix_tokens 40000`, `exploration_prefix_tokens_net 10000`, `rereads 3`
(cross-session 1, same-range 1, ranged 1), `wall_time_s 2090`, `active_time_s 350`
(190 + 70 + 90). The `src/big.go` file stat has 4 reads, 1 whole read and 9000 context
tokens: (33000−31000) + (35000−33000) + (36000−35000) + (35000−31000). A segment's first
request is never compared with the previous segment's last one. The step file
`src/steps_drv901.go` gets 4000 context tokens. With an escalation tagged
`context-auto-stop` at +1075s and a review at +2085s on the log, `ended_by` is
`context-auto-stop`, then `review`.
