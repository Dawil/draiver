---
section: Added
---
**`internal/pyramid` — load and validate `.test-pyramid.yaml` (drvctl-047).**
First rung of the test-pyramid epic (drv-009), data model only — nothing executes
yet. `pyramid.Load(worktreePath)` reads a repo's `.test-pyramid.yaml`: an ordered,
bottom-to-top `levels:` list of `{name, run}` entries, where the list order *is*
the pyramid and `Target()` returns the top (last) rung. A **missing file is a
valid "no pyramid" state** (`nil, nil`, like `config.Load`); a present but
malformed or invalid file is an error, so typos surface. Validation requires a
non-empty `levels`, unique non-blank names and a non-blank `run`. Unknown keys
are ignored, so later `up`/`down`/`healthcheck`/`timeout` fields can be added
without breaking older binaries.
