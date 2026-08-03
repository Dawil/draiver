# Draiver

> Agents are cattle, and it takes a village of agents to raise a ticket.

A Continuous Development tool that is a coordination substrate for supervising AI coding agents at the ticket level. It replaces the issue tracker's human-to-human status protocol with a human-to-agent one, on a single premise: agents are cattle, tickets are pets. You manage tickets, not agents. A fresh agent can pick up any ticket from disk, and a human attends only to the tickets that need them.

Issue Tracking software manage the interface between managers and developers. This aims to manage the interface between developers and agents.

Agents work well with CLIs (they have HATEOAS built in) and humans work well with UIs so this also has a Htmx web ui built in.

## Purpose

Externalize the valuable context — spec, decisions, escalations — so no agent's in-flight context is ever precious. One human supervises N tickets by attending only to escalations and reviews, not progress. Management by exception, at scale.

The current "development cycle" that is emerging is frontloaded and extensive design session with AI coding tools, that produce comprehensive designs as artifacts, either in an Issue tracker ticket or in a markdown file somewhere. Then you "babysit" AI agents, often in parallel on different tickets, until you have working PRs. The design at the front and the PR review at the back are the dev job, and the babysitting is new toil: managing context, loading tickets into agents, asking "Do you have everything you need", then jumping to the currently blocked AI agent to help advance it.

This tool is one thing leading to another:

1. Durable state/context, associated with the ticket, so that agents can be "stateless" (obviously they're intensely stateful, but let the context be an input)
2. Agent management comes naturally from the durable state, with swimlanes per ticket. 

## Attempts outlive the agent

An *attempt* is the progression of a ticket toward a PR — running `brief` to load
context, logging gotchas and decisions, escalating when blocked, applying
resolutions once answered. One attempt can span many escalate→resolve cycles;
escalation pauses the work, it doesn't abandon the ticket. What it need not span
is a single agent: the session that starts an attempt is rarely the one that
finishes it.

The point of draiver is that the attempt is not trapped inside the agent's
context window. When a session fills its context, drifts, or derails, you don't
lose the work. Kill the agent — it's cattle. Start a fresh session; it runs
`brief`, replays the spec and the log, and resumes exactly where the last one
left off: same decisions, same resolved escalations, no re-litigation. The log is
the memory; the agent is disposable. That resumption *is* the product.

Escalations are asynchronous. The agent runs `escalate`, which records the
question durably and returns a nonzero exit, and then its turn ends — it does not
sit and poll for an answer. The human isn't watching the CLI; they watch the
board (`draiver webui`), where the ticket surfaces under **Needs me**, and from
there they `resolve` it — via the CLI today, in-UI once manage mode lands.
Resolving is the human's move, not the agent's. The next attempt — resumed or
brand-new — re-enters through `brief` and reads the resolution inline. A blocked
ticket and its answer are one durable artefact, independent of whichever agent
picks it up next.

## Storage: append-only, portable per ticket

The ticket is the atomic, portable unit — team, project, and assignee are fields, not folders, so tickets move freely between them. Markdown files contain frontmatter. A folder per ticket; the shared `spec.md` sits at the top, and the mutable state lives under one folder per **attempt**.

```
PROJ-123/
  spec.md                 # frontloaded design — the immutable input, SHARED by every attempt
  attempts/
    0001/                 # one attempt = one journey from a starting point (its own working tree)
      attempt.md          # provenance: tool, model, actor, started (+ reserved room for metrics)
      log/                # append-only events, one write-once file each; its own hash chain
      artefacts/          # blobs the log references, never inlines
      state.md            # generated projection — do not edit
    0002/                 # a separate attempt: different tool/model, for comparison and audit
      attempt.md  log/  artefacts/  state.md
```

An **attempt** owns one log and one hash chain, and spans many respawned sessions (§ *Attempts outlive the agent*). Separate attempts are the comparison unit: run `claude-code` against `aider`, or re-try a stuck ticket, and each keeps its own durable trail against the same `spec.md`. Event type is a field, not a directory; structure lives in references between events, never in the tree. Corrections are new events; history is never rewritten.

## Vocabulary (CLI verbs = the shared protocol)

Discoverable via --help; the verbs are to agents what the status enum is to Issue Tracker.

* `brief PROJ-123` — replay spec + log into a context blob that cold-starts a fresh agent. If brief isn't enough to resume, the design is leaking state.
* `escalate` — append an escalation event and halt (nonzero exit). The gate is enforced by process control, not agent goodwill.
* `resolve` — the human's answer, appended and linked back. Escalation + resolution is one durable artefact.
* `inbox --mine` — unresolved escalations across all attempts of all tickets.
* `status` — regenerate the projection(s).
* `audit PROJ-123` - verifies the hash-chained log of every attempt (see below)
* `attempt new PROJ-123 --tool …` / `attempt ls PROJ-123` — start or list attempts. Verbs act on the ticket's latest attempt by default; `--attempt 0002` (or `DRAIVER_ATTEMPT`) targets a specific one.

## Control states (not progress states)

The dashboard columns are the human's relationship to the ticket, weighted asymmetrically. Control state is **per attempt**, so a ticket with two live attempts shows as two cards:

* `Running` — agent working, no action. Rendered as a count.
* `Needs me` — unresolved escalation. This is the board.
* `Review` — agent claims done; a claim, not a fact. Load-bearing.
* `Done.`

## Auditability: hash-chained log

Each event carries the hash of its predecessor, so any edit breaks the chain — tamper-evidence without git, and without git's container problem (which conflicted with mobile tickets). Attribution (actor, ts) lives in the event data, not in git blame.

## Persistence and speed

* Filesystem - default, e.g. `~/.draiver/data/`
* S3 — Easy sync. Completed tickets can be archived with zip → upload → drop from the working set. Versioning gives a second, independent tamper-evidence layer.
* SQLite — local, rebuildable cache/index over the canonical log. Powers fast agent queries and the board. Never the source of truth; the backend must never surface its schema in the verbs.

## Dashboard

UIs are for humans, so `draiver webui` will run a Htmx webserver (dynamic SPA, self contained within the cli tool). It can be used in a Read Only fashion, by just pointing at a data folder, or can be used in a Manage mode, being able to spin up agents, refresh agent context (kill agent and reload a fresh one with the same context), or restart ticket with a separate attempt if stuck (still append only, creates a new, potentially concurrent attempt on the ticket, possibly even with different inference model or coding agent). Renders the four control states. Joins to the Issue Tracker by ticket ID: the thin glanceable state syncs up for managers, the rich log stays down for agents and devs.

## Agent control: JSON-stream over stdio

When webui is run in Manage mode:

Agents are driven headless, not screen-scraped. The backend spawns one agent per ticket in an isolated worktree and speaks newline-delimited JSON both ways (--input-format/--output-format stream-json), relayed to the browser over WebSocket/SSE. Backend-agnostic via thin per-agent adapters (Claude Code, Aider, Codex, …).

Two hooks map straight onto the model: the session ID is the cattle mechanism — kill the process freely, keep the ID plus the log, respawn and --resume. The tool-permission callback is the escalation seam — route "may I?" to escalate instead of auto-approving, and the agent's own authority boundary becomes the human-in-the-loop gate.
