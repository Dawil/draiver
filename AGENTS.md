# AGENTS

When fulfilling a `drv-*` / `drvctl-*` / `drvweb-*` ticket:

- Add BDD acceptance tests for the feature, tagged `@<ticket-id>` (e.g. `@drv-042`). Every
  scenario must attach at least one artefact — `shoot(...)` for UI, `attachText(...)` for
  non-UI evidence (see `docs/acceptance-criteria.md`, `features/`, `internal/acceptance/`).
- Where it helps a reviewer, extend the review environment (`review-env/up.sh`) to seed
  data that demonstrates the feature.
- Write a changelog fragment, `changelog.d/<ticket-id>.md` (format in
  `changelog.d/README.md`): frontmatter naming the Keep-a-Changelog `section:`, then a bold
  one-line summary ending in `(<ticket-id>).` and prose — or `skip: <reason>` with no body
  when there is genuinely nothing to tell. **Don't edit `CHANGELOG.md` directly**: it is
  generated from the fragments (`go run ./cmd/changelog generate`), and the required
  `changelog` test rung fails without your fragment.

## Running tests

Rungs (`.test-pyramid.yaml`), bottom-up; bare `draiver test` climbs to `acceptance`:

| rung | runs | typical time |
|---|---|---|
| `changelog` | `go run ./cmd/changelog check` | seconds |
| `unit` | `go test ./...` | 5–9 min |
| `acceptance` | `go run ./features/acceptance` (needs a browser) | several min |
| `integration`, `evals` | advisory; run only on request | — |

- The full climb exceeds the 120s tool timeout. Launch it detached and park:
  `draiver test --detach && draiver park <ticket>`. The result wakes the attempt.
- **Never sleep-poll** (`sleep N; cat …` is blocked). For any other long one-off
  command, run it in the background and wait for its completion notification.
- Before the full climb, run `go test ./internal/<pkg>/...` on the packages you touched.
- **Every BDD scenario must attach an artefact** (`shoot(...)` or `attachText(...)`).
  `TestEveryScenarioHasAnArtifact` enforces it but skips without a browser, so a local
  unit run won't catch a missing one.

## Branches and scope

Your branch was cut from `main`, and `main` keeps moving while you work.

- **Compare your branch with three dots:** `git diff main...HEAD` (and
  `git log main..HEAD` for your commits). A two-dot `git diff main..HEAD` also shows
  everything that landed on `main` since your cut — as if your branch had deleted it.
- To see what `main` has gained since your cut: `git log HEAD..main --oneline`.
- **Before restoring or reverting files you didn't author**, run
  `git log --oneline -- <file>` on both sides (`main` and `HEAD`). Don't log a
  "regression" gotcha, and don't `git checkout main -- <file>`, without that check.
- **Feedback that goes beyond the spec** — a new control, another ticket's concern —
  `draiver escalate` to confirm scope before you write any code for it.
