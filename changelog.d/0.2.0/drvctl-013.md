---
section: Fixed
---
**Permission callback now actually activates, so `ctl start` sessions can do
work (`draiverctl` Tier 0, drvctl-013).** The permission gate could never fire:
headless `claude` was launched without telling it to route tool-permission asks
to the client, so the first `Write`/`Edit` an agent attempted was auto-denied
by `claude` itself (*"…you haven't granted it yet"*) and the attempt stalled —
the request never reached draiver's gate. (This surfaced only after drvctl-010
moved checkouts out of `.git`, uncovering the next, more fundamental block.) The
fix passes `--permission-prompt-tool stdio` on every session, which routes asks
over the stdio control protocol as `control_request{can_use_tool}` frames — the
seam `internal/gate` and `claudecode.Decide` were built for. The flag is
undocumented (dropped from `claude --help`) but live in `claude` 2.1.x; verified
against 2.1.216. The supervisor now also defaults `--permission-mode` to
**`acceptEdits`**: `claude`'s path-aware classifier auto-approves edits *inside*
the worktree (an attempt progresses without a human waving through every write),
while writes *outside* the checkout and other risky tools are still routed to
the gate and escalate. Covered by a new live gated-tool case in
`manual_smoke_test.go` (in-workdir write flows; out-of-workdir write hits the
gate seam and, denied, does not land).
