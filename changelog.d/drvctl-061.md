---
section: Added
---
**A stopping session now logs a handoff note before it is reaped, and a context auto-stop restarts once on its own (drvctl-061).** When the supervisor stops a live session — at `context_limit`, or on a `ctl stop` served by the owning `ctl up` — it first interrupts any in-flight turn and prompts the session to run `draiver log <ticket> --type note --tag handoff` with a structured note (Done / In progress / Uncommitted files / Gotchas / Don't redo), waiting up to `stop_grace_seconds` (default 120) before it reaps. The escalation is still written before anything else.

- **Auto-restart.** After a successful handoff at the context limit, the supervisor resolves its own escalation and brings the attempt back on a fresh session cold-started from the brief, so the note is the first thing the next session reads. `context_auto_restarts` (default 1) bounds this per attempt; the next trip escalates to a human as before.
- **`ctl stop`.** Hands the stop to a running `ctl up`, which runs the checkpoint, and reports whether a note landed. With no `ctl up` owning the session it reaps directly and says so; `--now` skips the checkpoint.
- **Config.** `stop_grace_seconds: 0` reproduces the old immediate reap everywhere. `ctl up` and session start warn when `context_limit` plus a 40K handoff reserve exceeds `context_window`.
