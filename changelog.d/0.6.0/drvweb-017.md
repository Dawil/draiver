---
section: Added
---
**Capability view — a ticket's `wants:` sub-fleet and its supervision dial on the
detail page (drvweb-017).** When a ticket carries `wants:` edges (a Capability),
its attempt detail page (`ticket.html`) gains a **Capability** panel listing each
wanted child with its latest attempt's control state — Pending folded in via
`DeriveDesired` plus the live-session probe, exactly as the board does — and, for
a Pending child, the `WaitingReason` gate peek ("waiting on X"); a wanted ticket
with no attempt yet shows "no attempt yet". Below it, a **Supervision** radio
dial (passthrough / pre-digest, with an `(i)` explainer) posts on change — no
Save button — to the new `POST /ticket/{id}/{attempt}/supervision` route, which
shells `draiver ctl supervision` (drvctl-042) so the web layer keeps one write
path, then swaps the panel in place with a "saved" confirmation. The dial
pre-selects the **effective** mode (per-attempt override folded over the config
default) and notes "inheriting default (…)" when no override is set; the default
is threaded in from `cmd/webui.go` through a new `WithSupervisionDefault` server
option. Panel and POST are both gated to Capabilities, so an ordinary ticket
neither shows nor accepts the dial. Graph editing remains drvweb-016's
Dependencies editor.
