---
section: Added
---
**Live "Agent Logs" stream on the attempt page, plus a capped "more" Spec
(drvweb-011).** The attempt detail page showed only the Spec and the ticketlog
Log; to watch what the agent was actually doing — tool by tool — a supervisor
had to drop to a shell and run `ctl logs -f`. A new collapsible **Agent Logs**
section sits between Spec and Log: collapsed by default (`<details>`), on expand
it opens a Server-Sent-Events stream (`GET /ticket/{id}/{attempt}/agent-logs`,
the web layer's first stream — everything else is htmx polling) that relays the
same content `ctl logs -f` prints, the agent stream interleaved with the
drvctl-027 `session/ctl.jsonl` health transitions. The handler **shells out to
`draiver ctl logs -f`** (the same write-path-reuse discipline as the log/resolve/
enable POSTs) and relays each stdout line as one SSE `data:` event, bound to the
request context via `exec.CommandContext` so collapsing the panel or leaving the
page kills the follow — one connection per expanded panel, none for a collapsed
one. Over a pipe the CLI renders **plain, uncoloured** text (`newStreamRenderer`
gates styling on `term.IsTerminal`), which is exactly what this ticket wants;
ANSI/role colouring is deferred. Lines are rendered into a monospace `<pre>` via
`textContent`, never the markdown→HTML path, so the untrusted session content
(arbitrary tool output + model prose) cannot inject markup. A hand-run attempt
with no session yet shows a quiet placeholder, not an error. Separately, a long
Spec no longer pushes the Log off-screen: the Spec is capped to a max-height with
a fade and a **more/less** toggle (pure CSS + a small JS toggle, no server change
— `SpecHTML` is already rendered and sanitised), and the cap is dropped entirely
when the spec is short enough not to need one.
