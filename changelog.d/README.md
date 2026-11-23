# changelog.d — per-ticket changelog fragments

`CHANGELOG.md` is **generated** from the files in this directory (drv-052). Don't edit
it by hand: write a fragment here instead, and the generator renders it.

## Writing your fragment

Every ticket adds exactly one file, `changelog.d/<ticket-id>.md` (for example
`changelog.d/drv-052.md`). It has a small frontmatter block naming the
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) section, then the entry in house
style: a **bold one-line summary ending in the ticket id in parentheses**, then prose.

```markdown
---
section: Added        # Added | Changed | Deprecated | Removed | Fixed | Security
---
**Short summary of the change (drv-052).** Prose describing what changed, why, and
where — the same style as the existing entries.
```

The body is plain Markdown, written flush-left. The generator turns it into a top-level
bullet (it adds the `- ` and indents the continuation lines), so nested lists and
paragraphs just work.

### Nothing to tell?

For a change with genuinely nothing user-facing to say, write a **skip** fragment — a
non-empty reason and no body. It satisfies the rung and renders nothing:

```markdown
---
skip: test-only refactor of the acceptance runner
---
```

## The `changelog` test rung

`.test-pyramid.yaml`'s base rung runs `go run ./cmd/changelog check`. It is required, so
the Review gate refuses an attempt without a fragment. It:

- determines the ticket id — `$DRAIVER_TICKET` (exported by `draiver test`), else the
  `draiver/<ticket>/<attempt>` branch, else `--ticket ID`; with none it **fails** rather
  than passing silently;
- fails, naming the expected path, unless `changelog.d/<ticket-id>.md` exists and is valid
  (parseable frontmatter, a known `section`, a non-empty body — or a non-empty `skip:`);
- validates **every** fragment, so a malformed one can't break the generator later.

Unknown frontmatter keys are errors (a typo'd `sectoin:` must not silently drop an entry).

## Generating CHANGELOG.md

```sh
go run ./cmd/changelog generate           # rewrite CHANGELOG.md from the fragments
go run ./cmd/changelog generate --check   # exit 1 if CHANGELOG.md is stale
go run ./cmd/changelog release 0.12.0     # cut a release (see below); --date defaults to today (UTC)
```

**When it's regenerated.** Ticket branches add a fragment and leave `CHANGELOG.md` alone —
if every ticket regenerated it, concurrent attempts would conflict on `## [Unreleased]`
again. It is regenerated explicitly: at release, or by anyone who wants it current on
`main`. The generator is a pure, deterministic function of this directory, so
regenerating is idempotent and never conflicts with itself.

**Releasing.** `release X.Y.Z` moves every unreleased fragment into `changelog.d/X.Y.Z/`,
writes `changelog.d/X.Y.Z/_release.md` (`date:` frontmatter), and regenerates. The
topmost released section is what `draiver --version` reports (drv-006), so commit the
regenerated `CHANGELOG.md` with the release.

**Where the version comes from.** There is no version constant: the binary embeds
`CHANGELOG.md` (`version.go`) and `version.FromChangelog` (`internal/version`) returns
its topmost `## [x.y.z]` header, skipping `## [Unreleased]`. A ticket that only adds a
fragment therefore does not change `draiver --version`; only a `release` does.

## Layout and ordering

```
changelog.d/
  _header.md            the preamble above every release section
  README.md             this file (never a fragment)
  <ticket-id>.md        an unreleased fragment
  <x.y.z>/_release.md   a released version: `date:` frontmatter, optional intro prose
  <x.y.z>/<name>.md     a fragment released in that version
```

Files starting with `_`, and `README.md`, are not fragments.

The rendered `CHANGELOG.md` is: the header, then `## [Unreleased]`, then each release
newest-first by semantic version. Inside each release, sections come in Keep a Changelog
order (Added, Changed, Deprecated, Removed, Fixed, Security); empty sections are left
out. Within a section, entries are ordered by fragment name:

1. ticket ids first, by prefix (`drv` < `drvctl` < `drvweb`), then by number ascending
   (`drv-9` < `drv-10`);
2. a ticket that needs more than one entry adds continuation fragments
   `<ticket-id>.2.md`, `<ticket-id>.3.md`, … which sort right after `<ticket-id>.md`
   (the rung only ever requires the bare `<ticket-id>.md`);
3. any other name sorts after every ticket, lexically. Pre-ticket history (0.1.0–0.2.0)
   is named `NN-slug.md`, so lexical order is its original order.
