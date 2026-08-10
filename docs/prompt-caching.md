# Prompt caching in draiver

> draiver does not *lack* prompt caching — it drives Claude Code, which caches
> automatically. The work is to stop the control plane from defeating that cache
> across tickets, make the caching observable, and enforce the fleet-wide
> prefix invariants that ad-hoc Claude Code usage cannot coordinate.

Source material this document is built on:

- Claude API prompt caching — <https://platform.claude.com/docs/en/build-with-claude/prompt-caching>
- How Claude Code uses prompt caching — <https://code.claude.com/docs/en/prompt-caching>
- Agent SDK, modifying system prompts (§ "improve prompt caching across users and machines") — <https://code.claude.com/docs/en/agent-sdk/modifying-system-prompts>

## The reframe

Because the current adapter drives Claude Code headless over stdio stream-json
(`draiverctl.md` "Agent transport"), every session draiver runs already gets
automatic prefix caching — cache reads bill at ~10% of input — with zero code in
draiver. The `cache_read` / `cache_creation` figures the meter already handles
(the drvctl-012 measurement fix) are draiver *observing* that caching, not
incidental noise.

So the goal is not "adopt prompt caching." It is:

1. **Stop defeating the cache the control plane already has** across tickets.
2. **Make it observable** — per attempt for diagnosis, and aggregated across
   attempts to see the actual (cross-ticket) win.
3. **Enforce the prefix invariants** — uniform launch flags, one canonical
   protocol prefix, a pinned adapter version — that only a control plane can hold
   consistent across a fleet.

## How the cache is organised (Claude Code)

Prompt caching is a **prefix match**: the API matches the start of each request
against content it recently processed; any byte change anywhere in the prefix
recomputes everything after it. Claude Code orders each request stable→volatile:

| Layer | Content | Changes when |
| --- | --- | --- |
| Tools | tool definitions | the loaded tool set changes |
| System prompt | core instructions, **working dir, platform, shell, OS, git branch + recent commits** | adapter upgrade, or any of those embedded values change |
| Project context | CLAUDE.md, memory | session start / `clear` / `compact` |
| Conversation | the brief, messages, tool results | every turn |

Sharing at any layer requires **every layer above it to be byte-identical**. Two
facts follow that dominate draiver's design:

- **The cache is effectively scoped to one machine + one directory.** The system
  prompt embeds the working directory and git status, so *"two sessions in
  different directories miss each other's cache — that includes worktrees of the
  same repository."* draiver runs worktree-per-session, so by default no two
  attempts share anything.
- **Respawn is cache-cold.** A fresh session starts with no hits; resuming after a
  Claude Code upgrade reprocesses the whole history uncached. draiver's cattle
  model (kill → re-brief) and its 150K reap threshold each force cold turns.

## The dependency spine

```
tools  →  system(core + machine/dir/git)  →  project(CLAUDE.md)  →  conversation(brief + run)
                    ▲
         the wall: this section embeds working-dir + git-status, so nothing
         below it shares across worktrees/machines until it is removed.
```

Everything worth doing is about that wall. There are exactly **two blockers** in
the whole space:

| Blocker | Unlocks | Cost |
| --- | --- | --- |
| **TTL + respawn discipline** | same-session hits across gaps/resumes | config drop-ins + restart-depth policy. Cheap. |
| **System-prompt control** (remove the wall) | cross-ticket / cross-worktree / cross-machine sharing of tools + system + protocol | **a CLI flag** (see below). Not a rewrite. |

draiver's nature — one agent per worktree, never simultaneous agents in one
directory, and a restart being a symptom of an *unproductive* session — means the
same-session gains are marginal. **The prize is the second blocker**, and it is
cheaper than it first appears.

## The opportunity ladder

Each rung shares a wider prefix than the one below it.

- **A — turn-to-turn within a live session.** Free today. No change.
- **B — across gaps/resumes within a session.** Blocked by TTL and respawn
  discipline. On a Claude *subscription* the 1h TTL is already automatic (it drops
  to 5m only in usage-credit overage). Change: `ENABLE_PROMPT_CACHING_1H=1` env
  guard; prefer cache-preserving `restart` depth; pin the adapter version.
  *Marginal for draiver by design.*
- **C — sequential/parallel sessions in the same worktree.** Not draiver's shape
  (worktree-per-session). Out of scope.
- **D — across worktrees of one repo (every ticket, every `race` competitor).**
  Blocked by the wall. **The big unlock.** Change: strip the dynamic
  system-prompt sections so the tools + system prefix is byte-identical across
  every worktree/ticket/machine on the repo.
- **E — draiver's invariant protocol across tickets.** The brief is
  `[invariant protocol] + [per-ticket spec + log]`. The invariant half is
  shareable but today rides in the conversation layer and pays a cold write on
  every ticket's cold-start. Change: hoist it into the (now-static) system prompt
  via append; leave spec+log in the brief.
- **F — cross-machine fleet + pre-warming.** Needs the Agent SDK for a clean
  `max_tokens:0` prewarm. **Out of scope here** (see "Deferred").

## The key finding: system-prompt control is a CLI flag

The Agent SDK exposes the exact lever — `excludeDynamicSections: true` on the
`claude_code` preset — which *"moves the per-session context (working directory,
git-repo flag, platform, shell, OS version, auto-memory paths) into the first user
message, leaving only the static preset and your append text in the system
prompt, so identical configurations share a cache entry across users and
machines."* The same page states the **non-interactive CLI equivalent**:
`--exclude-dynamic-system-prompt-sections`.

draiver already drives the CLI non-interactively. So rungs **D and E are reachable
by adding two flags to the existing `claudecode` adapter launch** — no Agent SDK,
no sidecar, no rewrite:

- `--exclude-dynamic-system-prompt-sections` → the preset + tool defs become
  byte-identical across the repo (rung D).
- `--append-system-prompt <static protocol file>` → draiver's invariant protocol
  is cached once per repo instead of cold-written per cold-start (rung E).

**What is guaranteed to share:** tools + system prompt (preset + protocol append)
— the bulk of the fixed cost paid cold on every ticket today. **What may not:**
CLAUDE.md sharing across worktrees, because `exclude-dynamic` relocates the
varying env context into the first user message, which may sit ahead of CLAUDE.md
in the conversation. CLAUDE.md is small; treat its sharing as a bonus to confirm
empirically, not a goal.

**Constraint — the append must be byte-invariant.** No ticket id, no timestamp,
nothing per-ticket may enter the append text, or the prefix re-fragments. Anything
per-ticket stays in the brief.

**Constraint — pin the adapter version.** A Claude Code upgrade shifts the
`claude_code` preset and busts the shared prefix *fleet-wide*, not per session.
The control plane must pin the `claude` version for a session's lifetime and gate
upgrades (`DISABLE_AUTOUPDATER`).

## Static prefixing: the verdict

It factors in, but **not as manual `cache_control` breakpoint placement** — that
lever is not draiver's under Claude Code, which does automatic caching with a
moving breakpoint. "Static prefixing" in draiver's world means two things, both
above:

1. Keep the prefix byte-stable (don't switch model/effort mid-session, pin the
   adapter version, keep the append invariant).
2. Relocate draiver's invariant content *above the wall* (into the system prompt
   via append) so it shares across tickets.

## Agent SDK vs. raw API — why both are out of scope for now

- **Claude Agent SDK** (`@anthropic-ai/claude-agent-sdk` / `claude-agent-sdk`)
  exposes the same lever as the CLI flag, plus non-caching benefits: programmatic
  `usage` (no stream-json scraping), in-process hooks/permissions, the beta
  cache-diagnostics feature, and `max_tokens:0` pre-warming. But it is
  **TypeScript/Python only — there is no Go Agent SDK.** For a Go daemon it means a
  draiver-authored Node/Python **sidecar** behind the existing adapter interface —
  a new runtime dependency that breaks the single-binary story. It is a clean,
  incremental *second* adapter (feature-flag, A/B against stdio, flip when proven),
  but it is justified by those non-caching benefits, **not** by the cache win,
  which the CLI flag already delivers.
- **Raw Messages API** would give literal `cache_control` static prefixing but
  only by making draiver its own coding agent — discarding Claude Code's harness
  and collapsing the multi-adapter design. Not worth it for this goal.

**Decision: capture the cache win via the CLI flags on the existing stdio adapter.
The SDK sidecar (with prewarm and cross-machine sharing) is deferred.**

## Measuring the benefit (cache visibility + A/B)

The billed cost per attempt mixes cache luck with the thing under test, so compare
on **cache-normalised work**, not billed cost:

| Metric | From meter fields | Isolates |
| --- | --- | --- |
| Cache-normalised work | `input + cache_creation + cache_read + output`, all at 1× | how hard the agent worked, cache discount removed |
| Billed cost | priced (`input×1 + cache_creation×1.25/2 + cache_read×0.1 + output`) | what you pay (keep, don't compare *on* it) |
| Protocol carry cost | `tokens(protocol) × (0.1×(turns−1) + 1.25)` | the standing tax of the appended prefix |
| Caching active | both cache fields > 0 | catches min-prefix / silent-invalidator misses |

**The measurement of *this* win is inherently cross-attempt.** A single attempt's
ratio cannot show amortisation; you need a per-repo rollup and flag-on vs flag-off
cohorts. This is the `race`/`pick` scoreboard applied to draiver's own config
rather than to model choice: same ticket, flag-on vs flag-off, compare normalised
cost and outcome. Caveat: LLM runs are stochastic — N runs per arm, not one; cache
warmth you can control, generation variance you can only average out.

## Roadmap (in scope)

Tiered like `draiverctl.md`, instrument → change → measure:

1. **Instrument.** Persist and price cache tokens per attempt; surface per-attempt
   in the UI.
2. **Change.** Add the CLI-flag config surface (default off); pin the adapter
   version; move the invariant protocol into the static append and slim the brief;
   guarantee the 1h TTL.
3. **Measure.** Cross-attempt / per-repo rollup with A/B cohorts; a validation run
   that proves the cost win *and* outcome parity, plus a silent-invalidator canary.

## Implementation tickets

| Ticket | Phase | What |
| --- | --- | --- |
| `drvctl-031` | instrument | Persist + price cache tokens per attempt (meter.json → attempt.md; `caching_active`) |
| `drvweb-012` | instrument | Per-attempt cache panel |
| `drvctl-032` | change | Cache config surface + CLI-flag plumbing (`--exclude-dynamic-system-prompt-sections`, `--append-system-prompt`), layered, default off |
| `drvctl-033` | change | Pin adapter version + gate autoupdate |
| `drvctl-034` | change | Move invariant protocol into static append; slim the brief |
| `drvctl-035` | change | Guarantee 1h TTL + confirm auth mode |
| `drvweb-013` | measure | Cross-attempt / per-repo cache rollup + A/B cohorts |
| `drvctl-036` | measure | Validation run + regression gate + silent-invalidator canary |

Minimum path to a measured result: `drvctl-031 → 032 → 033 → 034 → 036`, with
`drvweb-013` to read the verdict. `drvweb-012` and `drvctl-035` parallelise.

## Deferred (explicitly out of scope)

- **Agent SDK sidecar** (rung F enablers): programmatic usage, hooks,
  cache-diagnostics, and the runtime dependency it brings.
- **Pre-warming** (`max_tokens:0` in the reconcile loop) — needs the SDK path;
  marginal while ticket throughput keeps the shared prefix warm within TTL.
- **Cross-machine fleet sharing** for remote executors — depends on the SDK path
  and the `draiverctl.md` "future direction" runtimes work.

## Open questions

- **Adapter auth mode** (subscription vs API key) — decides whether the 1h TTL is
  already the default (subscription) or an opt-in (API key). Confirm before
  scoping `drvctl-035`.
- **150K reap vs Claude Code compaction** — reaping mid-task is a cache-cold event
  draiver *chooses*; Claude Code's in-session compaction keeps system+project
  warm. Worth revisiting whether the threshold should defer to compaction.
- **Exact minimum cacheable prefix** for the pinned model (~1K–4K tokens by
  model) — the tools+system prefix comfortably exceeds it, but `drvctl-031`'s
  `caching_active` flag should confirm rather than assume.
