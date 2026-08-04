# draiverctl — the supervisor for AI coding sessions

> systemd is two things bolted together: a declarative state store (units +
> journal) and a reconciling supervisor (PID 1 + `systemctl`). Draiver built the
> first. `draiverctl` is the second. Agents are cattle; this is the thing that
> raises and culls the herd.

## The split

Draiver today is systemd's *inert* half. The append-only, hash-chained log is
`journald`. `spec.md` + `attempt.md` are the unit file. The four control states
are unit states. `draiver webui` is a read-only `systemctl list-units`. What
`docs/mvp.md` explicitly deferred — "Manage mode: spinning up agents,
stream-json over stdio, WebSocket/SSE relay, worktree isolation, concurrent
re-attempts" — is systemd's *active* half: **PID 1 itself**, plus its client.

That daemon plus its client is `draiverctl`. The seam is clean because the
README already put it there: **the log is the source of truth, so the supervisor
holds no precious state and can itself be restarted** — the same reason systemd
survives `daemon-reexec`. `draiverctld` is cattle too.

```
draiver     = units + journal   (declarative state; already built; read-only)
draiverctl  = PID 1 + systemctl (the reconciling supervisor; this document)
```

## Why it's worth a supervisor (core benefits)

The onboarding skill teaches an agent the protocol; it cannot *enforce* it — a
drifting agent simply stops logging, and no one notices until the next cold-start
finds a thin log. `draiverctl` sits **inside the stdio loop**, a privileged seat
the skill never has, and that changes what's possible:

1. **Enforce the log by process control, not goodwill.** Because the supervisor
   relays the model's turns, it can *hijack the prompt and the response*: inject
   the `brief` on cold-start, and **withhold a tool call until the required log
   event is written** — refuse to forward the edit until the `decision`/`gotcha`
   is on disk. This is the same move `escalate` already makes (exit-3 gate
   "enforced by process control, not agent goodwill"), generalized to the whole
   protocol. The log stops being aspirational.
2. **Surface context usage live.** The supervisor sees every token in and out, so
   context-window consumption (and cost) becomes a live gauge in the UI — the
   human can see an agent approaching context exhaustion *before* it derails and
   pre-empt a refresh, rather than discovering a blown context after the fact.
3. **One-click session control.** With the daemon in place, the board's buttons
   become real: **start**, **stop**, **refresh** (kill + respawn from a fresh
   `brief`), and **new attempt** — the interactive `systemctl` the read-only
   board only gestures at today.
4. **Parallel sessions for comparison.** Isolated dev environments (worktree, or
   a container) per session let you run the same ticket under **different models
   or coding tools at once** and compare the resulting attempts — the tournament
   primitive below.

Everything else in this document is machinery in service of these four.

## Analogy table

| systemd | draiverctl | Meaning |
| --- | --- | --- |
| PID 1 / the manager | **`draiverctld`** — the supervisor daemon | Spawns sessions in worktrees, monitors, reaps, respawns. Holds no truth; rebuildable from the log. |
| `systemctl` | **`draiverctl`** (client verbs) | `start`/`stop`/`restart`/`status`/`enable`/`reload`, for tickets and attempts. |
| Unit file (`.service`) | `spec.md` + `attempt.md` + a **session spec** (adapter, model, permissions, env) | The declarative "what should run." `attempt.md` already carries tool/model — that is `ExecStart=`. |
| Template + instance (`getty@tty1`) | **ticket = template, attempt = instance** | `draiverctl start PROJ-123@0002`. The instance unit already exists. |
| Unit states (active / failed / …) | the four **control states** + session substates | Running↔active(running); Needs-me↔blocked; Review↔the claim; Done↔inactive(success). |
| journald / `journalctl` | the append-only hash-chained **log** | Built. Add `draiverctl logs -f` to tail live stream-json, promoting semantic events into the durable log. |
| `Restart=on-failure`, `RestartSec` | **respawn policy** — the "agents are cattle" core | Context full / crash / derail → kill, `--resume` a fresh session from `brief`. Maps 1:1 to the premise. |
| `WatchdogSec`, `sd_notify` | **liveness + progress watchdog** | No log event in N min, or looping/thrashing → reap and respawn. Heartbeat = log events. |
| `StartLimitBurst`, `reset-failed` | **respawn ceiling → escalate** | After K respawns/escalations in a window, stop looping and flip to Needs-me. The anti-runaway brake. |
| `Requires=` / `After=` / `Wants=` | **ticket dependency DAG** | Don't spawn B until A is Done/merged. Gate admission on deps. |
| Targets (runlevels) | **milestones / epics** | "Bring up all tickets for release X"; reached when constituents are Done. |
| Slices + cgroups (`CPUQuota`, `MemoryMax`) | **budget slices** (tokens/$ per ticket/project/team) + **concurrency cap** | Cost is the scarce resource. `TokenMax=`, `CostQuota=`, max-parallel. Enforced mid-run, not just at admission. |
| Socket / path activation | **on-demand + resolution activation** | The log dir *is* a `.path` unit: a new `resolution` event fires a resume session. |
| Timers (`OnCalendar`) | **scheduled re-attempts / nightly retries / periodic audit** | |
| `enable` / `disable` / `mask` | **in-fleet / parked / quarantined** | Whether the supervisor keeps a session on it; `mask` = never auto-pick (needs design first). |
| `isolate <target>` | **focus mode** | Run only this milestone; pause the rest to free budget/concurrency. |
| `daemon-reload` | **context refresh** | `spec.md` edited → respawn with a fresh `brief` (README's "kill + reload with same context"). |
| `ExecStartPre/Post`, `ExecStopPost` | **lifecycle hooks** | Pre: create worktree, run brief. Post-stop: run tests, open PR on `review`, clean worktree. |
| Scopes (adopt external procs) | **attach to a hand-started session** | A dev's manual `claude-code` run, adopted under supervision. |
| `systemctl --user` | **per-dev fleet vs shared team fleet** | Multi-tenant slices. |
| Drop-ins (`.d/`), `EnvironmentFile` | **layered agent config** | global → project → ticket overrides (model, tools, permission policy). |

## Where systemd's model doesn't reach (AI-native additions)

These have no clean systemd equivalent and are what make `draiverctl` more than a
rebrand.

1. **Progress ≠ liveness.** Processes don't drift; agents do. The watchdog can't
   just ask "is it alive," it must ask "is it moving *toward the ticket*."
   Detecting loops, thrash, and off-task wandering is a new subsystem — cheapest
   signal is log-event cadence and repetition; richer is a judge over the stream.
2. **Human attention is the constrained resource, not CPU.** systemd schedules
   against CPU/mem/IO. Here the objective is to *minimize enqueues to the
   "Needs me" column* — management by exception. The inbox is the human's
   run-queue; the supervisor's job is to keep it short. A genuinely different
   scheduler goal.
3. **Success is a claim a human ratifies, not exit 0.** `review` is
   `sd_notify READY=1` that a human must countersign. The supervisor never
   self-certifies Done. `ExecStopPost` can open the PR; the terminal transition
   is external.
4. **Instances race; they don't just scale.** Two attempts on one ticket are
   *competitors* (claude-code vs aider vs a re-try), not replicas. `draiverctl`
   needs a **tournament/selection** primitive — run N, pick the winning attempt,
   discard the rest — which systemd has no notion of.
5. **Cost enforcement is mid-flight.** cgroups meter continuously; you must be
   able to kill a session *at* its token budget, not merely refuse admission —
   and report the burn back into `attempt.md` (which reserves room for metrics).

## Decisions (locked)

| Area | Decision |
| --- | --- |
| Language | Go 1.26 (same module, `github.com/Dawil/draiver`) |
| Shape | One daemon `draiverctld` + one client `draiverctl`; ship as subcommands of the same binary (`draiver ctl …`) or a sibling binary sharing `internal/`. **Default: sibling verbs under the existing binary**; say the word to split. |
| Supervisor model | **Declarative reconciler**, not imperative spawn/kill. Desired state (which tickets should have a live, in-budget, progressing session) vs actual (what's running); the loop closes the gap. k8s-flavoured systemd. |
| Source of truth | The append-only log — unchanged. The daemon persists *no* authoritative state; a `draiverctld` restart rebuilds its view from disk + a scan of live processes it can re-adopt by session id. |
| Agent transport | Headless stream-json over stdio (`--input-format/--output-format stream-json`), one process per session, one git worktree per session. |
| The two hooks | **session id = the cattle handle** (kill freely, keep id + log, `--resume`); **tool-permission callback = the escalation seam** (route "may I?" to `escalate`, don't auto-approve). |
| Adapters | Thin per-agent interface (`Spawn`, `Resume`, `Kill`, `Stream`, `Interrupt`); Claude Code first, Aider/Codex behind the same seam. |
| Safety brake | Respawn ceiling (`StartLimitBurst` analog) is **on by default** — a runaway agent escalates to a human instead of looping. Non-negotiable. |

## Session lifecycle (the state machine draiverctld drives)

Control state (per attempt, derived from the log) is unchanged. `draiverctl`
adds *session* substates beneath `Running`:

```
        spawn                 brief loaded            watchdog trip
inactive ───▶ spawning ───▶ briefing ───▶ working ───────────────▶ reaping
   ▲                                          │  │                     │
   │ done(human)                    review    │  │ escalate            │ respawn
   │                                          ▼  ▼                     ▼
 Done ◀──────────────── Review          Needs-me                  respawning
                       (claim)         (blocked; human)               │
                                          ▲                           │
                          resolution ─────┘        StartLimit hit ────┘
                                                    → Needs-me (give up looping)
```

- `working → reaping` fires on crash, context exhaustion, budget hit, or a
  **progress** watchdog trip (no log event in N min / detected loop), never on a
  plain "still thinking."
- `reaping → respawning` unless the respawn ceiling is hit, in which case the
  attempt lands in **Needs-me** with a synthetic escalation ("gave up after K
  respawns — human needed").
- `Needs-me → working` is **path-activated** by a `resolution` event: the human
  resolves, the daemon wakes a `--resume` session. No polling on either side.
- `Review` and `Done` are the same load-bearing human gates draiver already
  defines; the daemon can open the PR on `review` but cannot self-transition to
  `Done`.
- `Review → Running` is a first-class **reopen**: logging a `decision` against a
  Review attempt sends it back to Running (the decision text is the recorded
  reason), and the reconciler re-admits it like any Running+enabled attempt — no
  escalate/resolve workaround. The reopen is human-initiated, so it is not a
  daemon self-transition.

## draiverctld responsibilities (the reconcile loop)

Each tick: read desired set (enabled tickets, their deps satisfied, within
slice budgets, honoring the concurrency cap), diff against the live process
table, and act:

1. **Admit** — for a desired-but-not-running attempt with deps met and budget
   left: create a worktree, run `brief`, spawn the adapter, record the session
   id.
2. **Watch** — consume each session's stream-json; promote semantic events
   (gotcha/decision/escalation/review) into the durable log; meter tokens, cost,
   and **context-window usage** (surfaced live to the UI); run the progress
   watchdog.
3. **Gate** — two enforcement seams, both by process control:
   - *Permission gate:* on a tool-permission callback, append `escalate` and halt
     the session (exit-3 semantics), moving the attempt to Needs-me.
   - *Protocol gate:* inject `brief` on cold-start, and **withhold a tool call
     until its required log event is written** — the "enforce the log" benefit.
     A decision/gotcha reaches disk before the edit it justifies does.
4. **Reap & respawn** — per policy, subject to the `StartLimit` ceiling.
5. **Activate** — on a filesystem `resolution` event, wake a resume session.
6. **Retire** — on `review`, run `ExecStopPost` hooks (tests, open PR); leave
   the human to `done`. Clean the worktree.

## Command surface (client)

Global flags mirror `draiver` (`--data`, `--actor`, `--attempt`).

| Verb | Purpose |
| --- | --- |
| `draiverctl up [--concurrency N] [--budget …]` | Start `draiverctld` and begin reconciling the enabled fleet. |
| `draiverctl down` | Drain: stop admitting, let live sessions checkpoint to the log, exit. |
| `draiverctl start <ticket[@attempt]>` | Spawn (or resume) a session for one attempt. |
| `draiverctl stop <ticket[@attempt]>` | Kill the session; keep id + log for later `--resume`. |
| `draiverctl restart <ticket[@attempt]>` | Reap + respawn fresh from `brief` (context refresh). |
| `draiverctl status [<ticket>]` | Live view: control state, session PID, model, tokens/$, **context-window %**, worktree, last event, watchdog health. |
| `draiverctl logs [-f] <ticket[@attempt]>` | Tail the session's stream (durable events + raw stream-json). |
| `draiverctl enable / disable <ticket[@attempt]>` | In-fleet / parked. Stored as an `enable`/`disable` log event (in the hash chain, replayed by `brief`). **Default is disabled**: `up` reconciles only attempts that are both `Running` *and* enabled, so a repo full of `Running` tickets never auto-spawns until each is opted in. (`mask` = never-auto-pick, deferred with the tournament.) |
| `draiverctl race <ticket> --tool a,b [--model …]` | Launch N competing attempts; later `draiverctl pick <ticket@attempt>` selects the winner. |
| `draiverctl focus <milestone>` | `isolate` — run only this target, pause the rest. |
| `draiverctl budget <scope> --tokens … --cost …` | Set a slice budget (ticket/project/team). |

## Storage additions

The log stays canonical. The daemon needs only *rebuildable* runtime state, kept
out of the hash chain:

```
<data-root>/
  PROJ-123/attempts/0002/
    session/                 # NOT in the hash chain — runtime, rebuildable
      session.json           # adapter, model, session-id, pid, worktree, started
      stream.jsonl           # raw stream-json tee (the "journal"); semantic events promoted to log/
      meter.json             # tokens, cost, context-window usage, watchdog counters (fold into attempt.md metrics on retire)
```

`attempt.md` already reserves room for metrics — the meter's final tally lands
there on retire, so a completed attempt carries its own cost/latency record.

## Implementation order

The MVP of `draiverctl` is **Tier 0 + the Tier-1 watchdog trio**. Everything
past it is scheduling sugar; the trio is what makes "walk away from N sessions"
true.

1. **Tier 0 — Manage mode is real.** `draiverctld` reconcile loop (one adapter,
   no scheduler), worktree-per-session, stream-json ingest, the two hooks,
   `start/stop/restart/status/logs`, and the `enable`/`disable` supervision gate
   (default disabled — `up` admits only `Running` *and* enabled attempts).
2. **Tier 1 — earns the systemd name.** Respawn policy + **progress watchdog** +
   **StartLimit ceiling → auto-escalate**; path-activation on `resolution`;
   concurrency cap + token/cost budgets metered mid-run and written to
   `attempt.md`.
3. **Tier 2 — fleet.** Dependency DAG gating admission; attempt **tournament**
   (`race`/`pick`); Aider/Codex adapters; milestones/targets, `mask`
   (never-auto-pick), `focus`. (`enable`/`disable` pulled forward into Tier 0.)
4. **Tier 3 — the UI half.** Fold all of it into `webui` Manage mode over
   WebSocket/SSE: one-click **start / stop / refresh / new attempt** on each
   card, a live context-window gauge, and side-by-side parallel attempts —
   turning the read-only board into an interactive `systemctl`.

## Open defaults (flag if you disagree)

- **Sibling verbs** under the existing `draiver` binary (`draiver ctl up`),
  sharing `internal/`, over a separate `draiverctl` binary.
- Respawn ceiling **on by default** (e.g. 3 respawns / 1h → escalate).
- Progress watchdog default: no log event in **10 min** → suspect; loop
  detection on repeated identical tool calls.
- Worktree per session under a per-repo base in the user cache dir (outside the
  repo, so it is neither in the working tree nor under `.git` — the latter trips a
  coding agent's auto-mode permission classifier); cleaned on retire.
- Claude Code is the first and reference adapter.

## Future direction (tentative): runtimes & distribution

> **Speculative — not part of the tiered plan above and not committed.** This
> sketches *where the seams might go* if sessions ever need to run somewhere
> other than the local machine (e.g. dev environments and agents in AWS). Tier 0
> stays local; the only reason to write it down now is so the early interfaces
> don't accidentally foreclose it. Everything here is provisional and may be
> dropped.

The open question: could a session's dev environment and agent run remotely
(Fargate/EC2) instead of as a local subprocess, without rewriting the control
plane? If so, there seem to be **two orthogonal seams** worth keeping distinct —
adding only the first does *not* get you remote execution.

- **Seam A — client ↔ control plane.** The webui and `draiverctl` CLI would stop
  touching the filesystem/process table directly and instead hit an HTTP/gRPC API
  mirroring the verbs. A **local** implementation embeds `draiverctld` in-process
  (`draiverctl up` → webui hits localhost); a **remote** implementation points
  the same clients at a hosted control plane. (Cf. `kube-apiserver`: one contract,
  many clients.)
- **Seam B — control plane ↔ runtime.** The reconcile loop wouldn't know *how* a
  session is realized — local subprocess in a worktree, or a Fargate task with an
  EBS volume. That would be a pluggable **Runtime driver**, distinct from the
  **Agent adapter** (drvctl-001); the two axes compose ("Claude Code on Fargate"
  vs "Aider locally"), with the adapter running *inside* the runtime. The nearest
  prior art is Nomad's task drivers, not systemd.

Tentative layering:

```
webui  /  draiverctl CLI            pure clients
        │  Seam A: control-plane API (HTTP/gRPC)
draiverctld  (reconcile loop, gates, watchdog)
        │  Seam B: Runtime driver
   ┌────┴─────┐
 local        aws          × Agent adapter { claude-code | aider | codex }
subprocess  Fargate/EC2
+ worktree  + EBS/EFS
        │
Data plane: append-only log   { fs | S3 }   the shared source of truth
```

Two interfaces would carry it (sketches, not signatures):

```go
// Seam B — where/how a session runs. Env is separate from Session on purpose:
// the Env (VM/container/worktree) can outlive the agent (cattle), so a warm
// respawn need not reprovision the box.
type Runtime interface {
    Provision(ctx, EnvSpec) (Env, error)          // worktree locally; container/EC2 remotely
    Spawn(ctx, Env, AgentSpec) (Session, error)   // start the adapter inside that Env
    Attach(ctx, SessionRef) (Stream, error)       // re-adopt a live session after a ctld restart
    Signal(ctx, SessionRef, Sig) error            // interrupt / kill
    Teardown(ctx, Env) error
}

// Seam A — the verbs as a network contract. local impl embeds draiverctld;
// remote impl is an RPC client. webui + CLI depend only on this.
type ControlPlane interface { /* Start/Stop/Restart/Status/Logs/Enable ... */ }
```

**Why this might stay cheap:** because the log is already the portable source of
truth, *reads* never need the control plane — only *mutations* do. The webui can
render a live board straight from the log backend (that is today's read-only
`draiver webui`; pointed at an S3 root it would work remotely with no server-side
execution). Seam A's API is only on the write path.

If it were ever built, two consequences look load-bearing:

1. **Single writer per attempt.** Remote executors should *relay* their
   stream-json to the control plane and let it own the log append — one writer, a
   coherent hash chain, and no data-backend credentials at the untrusted edge.
2. **The cattle boundary sits below the Env.** Separating `Provision` (env) from
   `Spawn` (agent) lets an agent be killed and `--resume`d into a *warm* remote
   env without paying to reprovision — the same "agents are cattle, envs are
   longer-lived" split, whether the env is a cheap worktree or an expensive VM.

Open worries if this is ever picked up: the cost meter gains an infra dimension
(compute hours alongside tokens); remote executors need repo credentials and
egress (a real security boundary the local runtime lacks); and the whole thing is
only worth it if local-first has already proven the model.

**The one cheap hedge**, even while shipping local-only: consider writing
drvctl-001/003/005 as *the local implementation of a `Runtime` driver* rather than
as the runtime itself, so "run in AWS" could later be "add an `aws` driver" rather
than a rewrite. Whether to pay even that small abstraction tax up front is
undecided.
