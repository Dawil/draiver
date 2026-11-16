---
section: Added
---
**`draiver log --by <parent>` — edge-authorised cross-ticket log (drvctl-054).**
The `log` sibling of `resolve --by` (drvctl-050): a coordinator can push durable
direction or context into a child it `wants:` — enabled children + `log --by`
(direct) + `resolve --by` (unblock) is how a manager assigns work. The write is
authorised by the edge itself: `Repo.LogBy` in `internal/repo` checks the parent's
`spec.md` `wants:` names the child and otherwise refuses with "does not want … not
authorized", writing nothing. Accepted facts land in the child's log stamped with
a `parent:<P>` actor. The bright line is enforced in the gateway, not the caller:
`log --by` may only write a fact (`gotcha`/`decision`/`note`/…), never `review`,
`done` or another verb-owned lifecycle event — direct is not ratify, so a parent
can never move a child Review→Done. The type check runs before the edge load, so a
forbidden type is refused without touching the data root.
