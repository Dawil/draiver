---
section: Changed
---
**The invariant draiver protocol moves above the cache wall into the
system-prompt append; the brief slims to spec + log (drvctl-034).** The protocol
text is identical for every ticket, but it used to travel in the conversation
layer, so every cold-start paid a fresh cache write for it. It now ships in the
coding agent's `--append-system-prompt`, which can be cached once per repo
(rung E in `docs/prompt-caching.md`). A new `internal/handbook` package embeds
the canonical, byte-invariant `handbook.md` (the onboarding `SKILL.md` body
verbatim) via `go:embed`, with `Content()` / `Version()` / `VersionOf()`
accessors. A drift test keeps it pinned to `SKILL.md`, and a byte-invariance
test stops per-ticket data from leaking into it. `resolvePromptCache`
(`cmd/ctl.go`) now uses `handbook.Content()` as the **default**
`SystemPromptAppend`. `append_system_prompt_file` in config overrides it, and an
empty override file turns it off. `exclude_dynamic_system_prompt_sections` stays
off by default and is still the opt-in lever for cross-ticket caching. The brief
drops its `workingInstructions` nudge, which the append now carries, so the
brief contains only the per-ticket spec and log replay. For A/B comparison and
reproducibility, the protocol version is recorded as
`session.Identity.ProtocolVersion` at spawn and folded into
`attempt.Meta.ProtocolVersion` (`attempt.md`) when the attempt retires.
