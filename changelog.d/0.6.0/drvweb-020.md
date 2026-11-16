---
section: Changed
---
**Web UI archive now goes through the `draiver archive` CLI verb (drvweb-020).**
`handleArchive` no longer appends the archive event itself; it shells the new
`runDraiverArchive` helper (`draiver archive --accepted|--abandoned --data
--actor --attempt -- <id>`, from drvctl-043), restoring the `draiver` binary as
the single write gateway for every board mutation. The written event is
byte-identical to drvweb-019's, and the configured web actor is honoured.
