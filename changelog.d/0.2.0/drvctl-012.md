---
section: Added
---
**Context-window auto-stop — a backstop that halts a runaway session
(`draiverctl`, drvctl-012).** A session that grows its context window without
bound keeps burning tokens (and money) until a human notices; `internal/limit`
makes the supervisor stop it on its own. It is a third enforcement seam beside
the protocol and permission gates: on every metered usage frame it measures the
live context-window fill against a configured threshold, and the first crossing
records a durable **`escalation`** (why + the numbers) then reaps the session.
Recording an escalation — rather than a note — is deliberate: it flips the
attempt to **Needs-me**, so the daemon parks it for a human instead of
re-admitting it straight back into the same runaway (Tier 0 has no respawn
ceiling); a later `resolve` resumes it from a fresh cold-start brief, i.e. a
clean context window. Wired through the shared `dispatch`, so both `ctl up` (the
daemon) and the foreground `ctl start`/`restart` enforce it. The threshold is
configurable with a sensible default (**150,000 tokens**) via a new operator
**config file** (`internal/config`): JSON at `~/.draiver/config.json` (override
`--config` / `$DRAIVER_CONFIG`), a missing file is not an error (defaults
apply), and precedence is explicit flag > config > built-in default. New flags
`--context-limit` (0 disables) on `up`/`start`/`restart` and a `--config` /
`--context-window` pair that now also default from the config file.
**Measurement fix (the load-bearing prerequisite):** verifying the "~2M-token
context" reported on the drvctl-009 run showed it was an *artifact*, not a real
window — the terminal `result` stream-json line's usage is **cumulative across
the whole turn** (its `cache_read` alone summed to 1.9M), and the adapter was
running it through the per-request `ContextTokens = input + cache_read +
cache_creation` formula. The real per-message context peaked at ~135K. So
`claudecode.normalizeResult` now zeroes the result line's `ContextTokens` (it is
not a context snapshot) and `watch.meter()` folds a zero-context frame as
**cost-only**, leaving the live gauge fed solely by per-request frames — without
which any threshold below 2M would have tripped at the first turn end and killed
every session (drvctl-012).
