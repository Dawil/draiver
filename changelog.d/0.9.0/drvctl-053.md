---
section: Added
---
**Manager brief — a `wants:` ticket is told it is a coordinator
(drvctl-053).** `brief.Build` (`internal/brief`) used to produce the same brief
for every ticket and stripped spec frontmatter, so a `wants:` coordinator could
not see its own children and fell back to implementing its spec itself — the one
thing a manager must not do. The brief now branches on whether the ticket has
`wants:` edges. A manager's brief gains a **`## Your team`** roster (each child's
control state, from the `project.Control` fold over its latest attempt, plus that
child's open escalations); the **coordinator protocol** (your spec is a charter,
not a work item; assess; `resolve --by` / `log --by` what you can; escalate
**once**, consolidated, only when a human is really needed; then `park`); and the
**wake reason** — the `woken-by` activation event from drvctl-051, rendered as the
freshest tail of the brief. A ticket with no `wants:` edges gets the worker brief
unchanged. This reverses the "no special brief" line in
`docs/capabilities-and-supervision.md` (design in `docs/manager-as-worker.md`).
Covered by `features/progressive-brief/manager-brief.feature` and unit tests.
