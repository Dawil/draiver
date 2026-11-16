---
section: Added
---
**Dependency edges in `spec.md` — `wants:`/`after:`/`requires:` (drvctl-037).**
The unit file gains its first *relational* fields: three optional frontmatter
lists of ticket ids — `wants:` (enable-propagation), `after:` (gate on a
predecessor reaching Review), `requires:` (gate on Done); semantics per
`docs/capabilities-and-supervision.md`. This is the **data model only** — no
reconciler consumes the edges yet, and `status`/reconciler output is unchanged.
A new authoring verb **`draiver depends TICKET --wants … --after … --requires
…`** merges edges into the spec's frontmatter the same way `draiver title`
edits `title:` — metadata *outside* the hash-chained log, so it never touches
`draiver audit`. Each flag is repeatable/comma-separated and **adds** to the
existing set rather than replacing it; the injected line is canonical inline
YAML (`wants: [A, B]`), replacing any prior inline-or-block entry while leaving
the rest of the spec — body, comments, untouched keys — intact. The model layer
exposes `project.ReverseWants` (who wants X), the substrate for reverse-wants
activation (drvctl-041). The three relations form a DAG: at author time a
**self-edge or any edge that would close a cycle is refused** with the offending
path (`A → B → A`), across the union of all three relation kinds, and a refused
edit rewrites nothing.
