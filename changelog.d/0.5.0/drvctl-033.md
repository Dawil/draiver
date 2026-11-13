---
section: Added
---
**Pin the Claude Code adapter version for a session's life and gate auto-update
(drvctl-033).** A background Claude Code upgrade shifts the preset system prompt
and tool definitions, busting the shared prompt-cache prefix fleet-wide and
silently eroding the drvctl-032/034 caching win on every release. The control
plane now holds that invariant. **Record:** a new `claudecode.Version` probes
`claude --version` (`2.1.216 (Claude Code)` → `2.1.216`) once at daemon start;
the reconciler pins it into every session's provenance as `AdapterVersion` in
`session.json`, folded into `attempt.md` as `adapter_version:` on retire beside
`protocol_version`. **Pin:** supervised sessions now launch with
`DISABLE_AUTOUPDATER=1` alongside `ENABLE_PROMPT_CACHING_1H=1`, so no
mid-session upgrade can occur — upgrades are operator-driven, between sessions.
**Attribute:** the drvctl-036 canary's `adapterVersion` loader (previously a
stub returning `""`) now reads the recorded version, so a busted turn-1 prefix
is attributed to a version bump rather than reported as "version unavailable".
A failed version probe is non-fatal — `draiverctld` logs it, records an empty
version, and the canary degrades to "unattributable" rather than refusing to
boot.
