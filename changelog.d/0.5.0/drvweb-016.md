---
section: Added
---
**Edit dependency edges from the web UI (drvweb-016).** The attempts-index page
(`/ticket/{id}`) gains a quiet **Dependencies** editor — the drvweb-009
provenance inline-edit style reused, but scoped to `spec.md` (ticket-level,
shared across attempts) rather than per-attempt `attempt.md`, since edges are
spec-level design input. Each relation (`wants`/`after`/`requires`) prefills as
a comma-separated id list you edit in place and save on blur — no Save button —
with an `(i)` tooltip explaining its reconciler semantics. The write shells the
drvctl-037 authoring verb (the web layer never writes `spec.md` itself, its
first web writer of spec frontmatter); a **cycle or self-edge refusal renders
inline** and writes nothing to disk. Because the field is a whole-set WYSIWYG
editor, the verb gains a **`--set` flag**: each relation you pass *replaces* its
set instead of adding to it (a blank value clears that relation; omitted
relations are untouched), so deleting an id in the UI actually removes the edge.
