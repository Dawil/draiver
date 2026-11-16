---
section: Added
---
**Protocol gate — enforce the log by process control, not agent goodwill
(`draiverctl` Tier 0, reconcile loop step 3).** `internal/protocol` is the
supervisor's second gate, alongside the permission gate: it enforces the
draiver protocol the onboarding skill can only ask for. Two halves. **Cold-start
brief** — `InjectBrief` builds the attempt's `brief.Build` and `Prompt`s it into
a freshly spawned (or resumed) session behind a short supervisor preamble, so an
agent is *always* briefed rather than trusted to fetch it (`manage.Handle`
satisfies the `Prompter` seam). **Withhold-until-logged** — `Gate.Consider`
intercepts the agent's tool-permission callback and refuses to forward a
mutating tool until a `decision`/`gotcha` justifying it is on disk: the
rationale reaches the durable, hash-chained log *before* the edit it explains
does. The gate is a **veto, not an approver** — it only ever denies (withholds)
and returns `Cleared` without answering when a tool is `Free` or already
justified, so it composes as protocol → permission with no double-answer;
allowing/escalating stays the permission gate's job. A withhold is transient,
not an escalation: the session stays live so the agent logs its rationale and
retries the same edit — nothing is halted, no escalation event is written. A
justifying event must be **newer than the session baseline** (the log tail seq
snapshotted at `New`), so each driven session justifies its own edits rather
than riding a stale rationale. A layered `Policy` (`Layer(global, project,
ticket)`, mirroring the permission gate) with an `Edits()` base marks
`Edit`/`Write`/`NotebookEdit` `Justified` and leaves everything else `Free` —
`Bash` deliberately stays `Free` (the agent runs `draiver log` *through* it, so
gating it would deadlock) and remains gated to a human by the permission gate's
`ReadOnly` base instead (drvctl-007).
