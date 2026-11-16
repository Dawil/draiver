---
section: Added
---
**Session runtime store (`draiverctl` Tier 0).** A per-session `session/`
directory under an attempt — `session.json` (identity: adapter, model, session
id, pid, worktree, started), `meter.json` (cumulative token/cost/context usage
plus watchdog counters), and `stream.jsonl` (the raw stream-json tee). It is
rebuildable and deliberately kept **out of the hash-chained log**: whole-file
records are replaced atomically (temp-file + fsync + rename) and `stream.jsonl`
is append-only and safe for concurrent appends (`internal/session`, with paths
in `internal/store`).
