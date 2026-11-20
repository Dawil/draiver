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
