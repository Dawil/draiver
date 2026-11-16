---
section: Added
---
**Compose typed log entries from the webui, with Review → Decision / Done
actions (drvweb-006).** The attempt detail page gains a compose box — a type
`<select>` (`note`, `gotcha`, `decision`) plus a markdown body — that appends a
typed event to the attempt without dropping to a terminal, and two Review-gated
buttons: **Decision** (reopens a Review attempt to Running) and **Done** (closes
it), both driven by the existing lifecycle `Derive`. This makes the board a
write surface for the first time: `POST /ticket/{id}/{attempt}/log` performs
the append by shelling the draiver CLI (`draiver log --type …`, and `draiver
done` for the terminal action) rather than reimplementing it in the web layer,
so the webui and a human at a terminal share one append path. It passes a
resolved actor (`$DRAIVER_ACTOR`, else `human:$USER`, else `human:web`) through
as `--actor` so a web-composed entry is attributable exactly like a CLI one. The
type is checked against a curated allow-list `{note, gotcha, decision, done}`,
so lifecycle types with their own flows (`escalation`/`resolution`/`review`/
`enable`/`disable`) can never be hand-typed (400, nothing appended). The
state-changing POST is guarded by an Origin/Referer same-host check (403 on
cross-origin; header-less non-browser clients pass). One response re-renders the
log region and OOB-swaps the state badge and log count, so a Decision/Done
visibly flips the badge, and a Done then self-cancels the log poll on its next
`/live` tick (286). The `web` package is now read-mostly rather than read-only.
