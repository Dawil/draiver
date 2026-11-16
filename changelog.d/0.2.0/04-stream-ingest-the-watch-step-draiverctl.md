---
section: Added
---
**Stream ingest — the "Watch" step (`draiverctl` Tier 0).** The reconcile
loop's step 2: a `Watcher` consumes one session's normalized event stream and
does its two jobs (`internal/watch`). **Promote** — semantic protocol events
the agent emits (gotcha / decision / escalation / review) are recognized in the
stream and appended to the attempt's durable, hash-chained log via
`internal/ticketlog`, so the supervisor is the single writer. A `Recognizer`
seam with a reference `ProtocolRecognizer` lifts the `draiver log|escalate|
review` calls the onboarding skill teaches — no new agent-facing protocol —
and refuses (rather than corrupts the log) on any command whose body it cannot
reproduce losslessly (command substitution, heredocs, pipes/redirects).
**Meter** — every raw stream-json line is teed once to `stream.jsonl` (deduping
the sibling events one line fans out into), and token/cost/**context-window**
usage is folded into `meter.json`: context tracks the latest snapshot as a live
gauge while cost is folded monotonically so it never regresses on the zero-cost
per-message frames between turn ends. Promotions advance the meter's
progress-watchdog heartbeat (drvctl-004).
