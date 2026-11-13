---
section: Changed
---
**Readable `ctl logs` — a fixed-width `[time · tokens · role]:` prefix and
Markdown-rendered prose (drvctl-025).** The shared session renderer marked each
line with ad-hoc glyphs, and the assistant's prose printed as raw Markdown
source. Every line now carries a fixed-width, greppable
**`[<datetime>  <tokens>  <role>]: <content>`** prefix, and assistant prose is
rendered from Markdown to styled ANSI via `charmbracelet/glamour` (over the
goldmark parser already vendored). `renderEvent` moves onto a stateful
`streamRenderer` so the tokens column carries the **last-known cumulative
context-window snapshot** forward across the many events that report no usage —
a cost-only `result` frame (ctx=0) no longer clobbers it. Roles map to a small
vocabulary (assistant/tool/system/perm/error), each tinted on a TTY — **only
the role token is coloured**, the datetime, token tally, and brackets stay
plain, with the role column hand-padded so the closing bracket still aligns past
the colour escapes — and tool output is italicised; usage-only frames print
nothing (their figures ride the next line's prefix). The timestamp is
render-time wallclock (the stream carries none), and `--json` / `stream.jsonl`
stay byte-for-byte unchanged. Non-TTY output uses glamour's notty style so no
ANSI leaks into a pipe or `jq`; a bespoke ANSI-aware trim strips glamour's
per-cell colour padding on a TTY. One renderer is still shared by the live
follow and disk replay.
