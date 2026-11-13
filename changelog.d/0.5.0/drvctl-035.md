---
section: Changed
---
**Pin the 1-hour prompt-cache TTL for every spawned session; record the adapter's
auth mode (drvctl-035).** The daemon-level `BaseSpec.Env` built in `newReconciler`
(`cmd/ctl.go`) now carries `ENABLE_PROMPT_CACHING_1H=1`, which flows unchanged
through `reconcile` → `manage` → the Claude Code adapter onto both spawn and
resume, so the shared per-repo prefix stays warm across gaps longer than the
default TTL (between tickets on a repo, a session parked on Needs-me). The adapter
was confirmed to authenticate via a Claude.ai subscription (OAuth, no
`ANTHROPIC_API_KEY`), where the 1h TTL is automatic but silently drops to 5m in
usage-credit overage — so the drop-in is a real opt-in, not a no-op. The adapter
itself stays auth-agnostic. `docs/prompt-caching.md` gains a "Rung B in practice"
section with the per-auth-mode TTL table and where the pin is wired, and its open
"adapter auth mode" question is marked resolved.
