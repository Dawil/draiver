---
section: Changed
---
**The injected brief now carries the Markdown-when-logging nudge (`draiverctl`,
drvctl-020).** The board renders event bodies as Markdown, but that how-to-log
guidance only reached agents that loaded the onboarding skill (§3.5) — sessions
the supervisor drives get their instructions from the cold-start brief, which
carried none, so a supervised agent without the skill logged unstructured blobs.
`brief.Build` now prepends a one-line working-instructions preamble,
single-sourced from a `workingInstructions` const kept in sync with SKILL §3.5.
It rides every cold-start and resume (`protocol.InjectBrief` re-injects the
brief after Spawn/Resume) and sits with the spec/log the agent already reads.
