---
section: Added
---
**Self-documenting attempt.md/spec.md frontmatter and `draiver attempt set`
to edit provenance after creation (drvctl-028).** The provenance fields that
gate the whole land flow (`repo:`/`base:`, which `ctl merge`/`sync` demand)
used to vanish from `attempt.md` the moment they were unset — `omitempty` plus
a whole-struct `yaml.Marshal` dropped the key entirely, so a legacy or
repo-unresolved attempt gave a hand-editor *no hint the fields even exist*. And
there was no verb to set them after `attempt new`: fixing a Review attempt
wedged for want of a `base:` meant recreating it (losing its log/branch) or
hand-editing YAML. Now `writeMeta` hand-renders the block: a set field renders
as a real line, an **unset** optional field (`repo`/`base`/`tool`/`model`)
renders as a commented example naming the key, a sample value, and what
consumes it (e.g. `# base: main  # branch this attempt lands into; used by
ctl merge/sync`). `parseMeta` ignores YAML comments, so a write→parse→write
cycle is idempotent. `scaffoldSpec` gets the same treatment for spec.md's
optional `project`/`team`/`assignee` board-grouping metadata. The new
**`draiver attempt set <ticket[@attempt]>`** verb (flags `--repo`, `--base`,
`--tool`, `--model`) writes those fields into an existing `attempt.md` — the
exact analogue of `draiver title` on spec.md: static metadata, so it appends
**no** log event and never touches the audit chain, taking effect on the next
board/merge read. It is a *targeted* setter — only an explicitly-passed flag
overwrites its field, so `attempt set X --base main` never clobbers `repo` —
reusing `attempt new`'s validation (`--repo` required-if-given; `--base`
defaults to the repo's current branch via `resolveBase` when passed empty).
This verb is the write path a future webui provenance form shells out to
(drvweb-009).
