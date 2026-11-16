---
section: Added
---
**Set an attempt's repo/base from the board — the provenance table
(drvweb-009).** A Review attempt wedged for want of a `base:`/`repo:` (the two
fields `ctl merge`/`sync` gate on) could only be unblocked by hand-editing
`attempt.md` or recreating the attempt — losing its log and branch. A quiet
key/value **provenance table** now sits above the Spec on the attempt detail
page, showing the attempt's `repo`/`base`; a Review attempt that records no
base gets a merge-blocking callout. Each value reads as plain text until
clicked, when it becomes an inline input — the enclosing form posts on any
child's `change` (a blur that altered the value) or on Enter, so clicking out
saves and there is **no Save button**; an (i) bubble per key carries a
hover/focus tooltip explaining what that field gates. The write is not
reimplemented in the web layer: `POST /ticket/{id}/{attempt}/provenance` shells
**`draiver attempt set`** (drvctl-028), which owns the `attempt.md` write and
the field validation, keeping the package's write invariant that the web layer
never touches `attempt.md`. Only `repo`/`base` are read from the form (the
`tool`/`model` fields the verb also accepts are deliberately out of the UI's
scope, the same allow-list discipline the compose box uses); the panel prefills
current values, so a blank field means *leave unchanged* — it is omitted from
the shell-out, and an all-blank submit is a 400 rather than a 500 from the
verb's "nothing to set". No `--actor`: `attempt set` appends no hash-chained
event. On success the panel re-renders in place (htmx swap) with the saved
values and a confirmation, behind the same-origin and 404 guards as the other
writes.
