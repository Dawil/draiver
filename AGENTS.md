# AGENTS

When fulfilling a `drv-*` / `drvctl-*` / `drvweb-*` ticket:

- Add BDD acceptance tests for the feature, tagged `@<ticket-id>` (e.g. `@drv-042`). Every
  scenario attaches an artefact: a screenshot for UI, captured text otherwise. **Start from
  [`internal/acceptance/README.md`](internal/acceptance/README.md)**: step anatomy, the
  `World`, artefacts, a table of the shared helpers with `file:line`, a copy-paste
  skeleton and how to run it. You don't need to read the other `steps_*.go` files. Why
  criteria become features: `docs/acceptance-criteria.md`.
- Where it helps a reviewer, extend the review environment (`review-env/up.sh`) to seed
  data that demonstrates the feature.
- Write a changelog fragment, `changelog.d/<ticket-id>.md` (format in
  `changelog.d/README.md`): frontmatter naming the Keep-a-Changelog `section:`, then a bold
  one-line summary ending in `(<ticket-id>).` and prose — or `skip: <reason>` with no body
  when there is genuinely nothing to tell. **Don't edit `CHANGELOG.md` directly**: it is
  generated from the fragments (`go run ./cmd/changelog generate`), and the required
  `changelog` test rung fails without your fragment.
