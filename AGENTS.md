# AGENTS

When fulfilling a `drv-*` / `drvctl-*` / `drvweb-*` ticket:

- Add BDD acceptance tests for the feature, tagged `@<ticket-id>` (e.g. `@drv-042`), and
  capture screenshots where the feature is user-visible (see `docs/acceptance-criteria.md`
  and the `features/` tree).
- Where it helps a reviewer, extend the review environment (`review-env/up.sh`) to seed
  data that demonstrates the feature.
- Write a changelog fragment, `changelog.d/<ticket-id>.md` (format in
  `changelog.d/README.md`): frontmatter naming the Keep-a-Changelog `section:`, then a bold
  one-line summary ending in `(<ticket-id>).` and prose — or `skip: <reason>` with no body
  when there is genuinely nothing to tell. **Don't edit `CHANGELOG.md` directly**: it is
  generated from the fragments (`go run ./cmd/changelog generate`), and the required
  `changelog` test rung fails without your fragment.
