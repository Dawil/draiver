---
section: Changed
---
**`ctl logs -f` tails the last 50 records instead of replaying the whole history
(drvctl-030).** Follow mode used to replay an attempt's entire `stream.jsonl`
from byte 0 before going live — merely verbose at a terminal, but the webui
Agent Logs panel shells out to exactly this command over SSE, and a few thousand
back-to-back lines (one DOM reflow each) froze the page with the `no session
stream yet…` placeholder stuck on screen. `-f` now has `tail -f` semantics: it
starts from the last N **raw stream records** (not rendered lines) and then
follows. A new **`--tail N`** flag tunes the window — `50` is the default, `0`
shows no backlog (only lines appended after the follow starts), `-1` restores
the full-history replay. The window is follow-only (plain `ctl logs` still cats
the full history and ignores `--tail`), applies to the `stream.jsonl` backlog
only (sparse `ctl.jsonl` health lines are kept in full), and is identical for
the rendered and `--json` paths — the raw path seeks to a record boundary so its
byte-for-byte contract holds. A new `tailOffset()` helper in `cmd/ctl.go` finds
the start of the last N complete records and `lineReader` applies it once at
open. The panel hang is cured without any web change, since `handleAgentLogs`
passes no `--tail` and so inherits the default.
