---
section: Added
---
**Red error dot on the board when draiverctld can't run an attempt
(drvweb-008).** A `Running` + enabled attempt the daemon cannot bring up used
to render as healthy/idle — worst of all, a `restart --new-session` wedge sat at
`session_id=""`, the exact case the session-liveness dot drew as *nothing*. The
dot now also reads the per-attempt health log from drvctl-027
(`session/ctl.jsonl`): it lights a distinct crimson
(`Waratah`, a hue apart from the rust reserved for Stuck) with a
`title`/aria-label when any daemon error class is currently open in
`session/ctl.jsonl`. The rule is exactly the log's edge-triggered shape —
most-recent-transition-per-class-wins, so a self-healed blip that already logged
its `error-end` shows nothing — and it is **class-agnostic**: any open class
lights the dot (a `worktree-clash`, or the `admit-failed` catch-all that covers
the corrupt-object variant), so no real wedge renders as nothing. The error dot
is checked before the empty-`session_id`/no-`session.json` early-returns so the
founding wedge is visible, and outranks stopped/disabled/none; a live pid still
wins (a running process and a run-failure are mutually exclusive). The web
server stays strictly read-only and out-of-process — it `os.ReadFile`s
`ctl.jsonl` itself, exactly as it already reads `session.json` and probes pids,
never talking to the daemon.
