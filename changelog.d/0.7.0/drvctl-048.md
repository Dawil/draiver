---
section: Added
---
**`draiver test [RUNG] [--log]` — deterministic test-pyramid executor
(drvctl-048).** Run from inside an attempt worktree, `draiver test` loads the
repo's `.test-pyramid.yaml` (via drvctl-047's `internal/pyramid`) and climbs from
the base rung up to `RUNG` (default: the top), streaming each rung's command and
stopping at the first non-green one; a repo with no pyramid reports "nothing to
test". **Bare**, it only runs and reports — the fast iterative loop, fine on a
dirty tree. **With `--log`** it refuses a dirty tree up front and, only when every
rung through `RUNG` is green, appends one hash-chained **`test-result`** event
carrying the rung and HEAD commit, so `brief` replays "rung green @ sha" for the
next agent; a failing run logs nothing. The attempt is identified from the
checkout itself (new `internal/worktree/identify.go` — `Identify`, `HeadSHA`,
`DirtyAt`) rather than `$DRAIVER_ATTEMPT`. The new event's `Rung`/`Commit` fields
are `omitempty`, so every other event still hashes byte-identically. The handbook
and onboarding `SKILL.md` gain the iterate → commit → `test --log` → `review` step.
