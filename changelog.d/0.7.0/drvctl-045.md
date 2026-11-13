---
section: Fixed
---
**Sessions no longer die at spawn on the stored short model id — `opus-4.8` is
resolved to `claude-opus-4-8` before `claude --model` (drvctl-045).** draiver
stores models in a short form (`opus-4.8`, as the `--model` flag help and the
`attempt.md` writer document), but the Claude Code adapter passed it verbatim to
`claude --model`, which rejects it ("There's an issue with the selected model"),
so every daemon-spawned session failed before starting. `baseArgs` in
`internal/agent/claudecode` now routes the model through a new `resolveModel`
helper backed by a small `modelAliases` map (`opus-4.8` → `claude-opus-4-8`),
kept next to `DefaultBin` at the Claude-specific seam. Unmapped values — full
`claude-…` ids and the CLI's bare aliases (`opus`, `sonnet`, `haiku`) — pass
through unchanged, and an empty model still emits no `--model` flag. Spawn and
Resume both go through `baseArgs`, so a model from either `session.json` or
`attempt.md` is covered; the earlier one-off data rewrite of existing attempts is
no longer needed for new ones. `TestBaseArgsModelResolution` covers the map hit,
pass-through and empty cases.
