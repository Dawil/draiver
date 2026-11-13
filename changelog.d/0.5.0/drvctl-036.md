---
section: Added
---
**`draiver canary` — a silent-invalidator canary for the shared prompt-cache
prefix, plus the flag-on/flag-off validation run (drvctl-036).** The capstone of
the prompt-cache change set (drvctl-032/033/034). A new read-only verb, **`draiver
canary`** (backed by `internal/canary`), scans each repo's attempts and checks
that the second and later attempts reuse the shared tools+system+append prefix on
**turn 1** — keyed on the first usage frame in `stream.jsonl`, since within-session
caching swamps the session-aggregate meter and cannot tell a healthy prefix from a
busted one. When reuse attempts start cold-writing the prefix (per-ticket data
slipped into `--append-system-prompt`, an unpinned adapter upgrade) the repo is
marked `FIRE` and the verb **exits 5**. Per attempt it prints a table — turn-1
read/create, **`t1-rd:cr`** (turn-1 `cache_read/cache_creation`, the unbounded
">100%" amortisation metric), read share, hit ratio, normalised work, billed
input and a verdict (`ok`/`COLD`/`CACHING-OFF`) — so it doubles as the per-repo
cross-attempt cache rollup. Flags: `--repo` scopes to one repo, `--min-prefix`
(default 1024 tokens) and `--read-share-floor` (default 0.5) tune the cold
threshold. Adapter-version attribution is a seam that reports "version
unavailable" until drvctl-033 records version events. The new
**`docs/cache-validation.md`** records the protocol and verdict: over six on-arm
attempts with `exclude_dynamic_system_prompt_sections` on, turn-1 cache reads
grew (16.8K→17.6K), turn-1 cold-write fell 17%, turn-1 read:creation rose from
228% to 335%, and cwd/git/path behaviour showed 6/6 parity — so the flag was made
the operator default in `~/.draiver/config.json` (the code default in
`internal/config` is unchanged).
