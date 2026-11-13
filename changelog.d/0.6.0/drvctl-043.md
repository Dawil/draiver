---
section: Added
---
**`draiver archive` / `draiver unarchive` — first-class CLI verbs for board
archive, with sentiment (drvctl-043).** Board archive (drvweb-019) was the one
webui write with no CLI verb, and the only terminal route — `draiver log … --type
archive` — could not set the `Outcome` field, so a CLI archive was
sentiment-blind. The new **`draiver archive <ticket[@attempt]>`** appends an
`archive` event with **`--accepted`** (the green-tick sense) or **`--abandoned`**
(the grey-cross sense); with neither flag it defaults to the State-derived
sentiment exactly as the board does (Done → `accepted`, else `abandoned`), and
passing both is rejected before any write. **`draiver unarchive
<ticket[@attempt]>`** appends an `unarchive` event (no outcome; last-wins via
`DeriveArchived`), returning the attempt to its lifecycle column. Outcome strings
and the `archiveBody` log prose match `internal/web` verbatim, so board- and
CLI-authored archives are indistinguishable in the log and to metrics. Both verbs
are idempotent — re-archiving an archived attempt or unarchiving a live one
reports the current state and exits 0 without a second event — and a missing
ticket/attempt errors and writes nothing. Target parsing reuses
`resolveCtlTarget`; `cmd/root.go`'s `appendEvent` is split into a resolver plus a
new `appendEventAt` core the verbs share. The webui repoint is drvweb-020.
