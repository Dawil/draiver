---
section: Added
---
**Prompt-cache config surface — `exclude_dynamic_system_prompt_sections` and
`append_system_prompt_file` (drvctl-032).** Two new daemon config keys, both
default off/empty, unlock cross-worktree/cross-ticket prompt-prefix sharing
(`docs/prompt-caching.md`, the M0 unlock). `exclude_dynamic_system_prompt_sections`
(bool) makes the Claude Code adapter pass `--exclude-dynamic-system-prompt-sections`,
moving the per-machine system-prompt sections (cwd, env, memory paths, git status)
into the first user message so every worktree shares a byte-identical prefix;
`append_system_prompt_file` (path) supplies draiver's protocol text as
`--append-system-prompt`. The file is read **once** when `ctld` builds its
reconciler (`resolvePromptCache` in `cmd/ctl.go`), so the appended text is
byte-uniform across every attempt; an unreadable file is a hard error rather than
a silently empty prefix. With the exclude toggle on, start-up probes the pinned
`claude` (`claudecode.SupportsExcludeDynamicSystemPrompt`) and **fails loud** if
it does not accept the flag (pairs with the drvctl-033 version pin). With both
keys unset the launch is byte-identical to before. Attempt-level A/B cohorts are
left to drvctl-036/drvweb-013.
