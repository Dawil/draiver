# Cache validation run + regression gate (drvctl-036)

> The capstone of the prompt-caching work (`docs/prompt-caching.md`): prove the
> flag change set delivers the cross-ticket cache win **and** does not regress
> agent behaviour, before the flags become the default. Plus a standing canary
> that catches a future silent prefix-bust.

This document is the **methodology and the verdict template**. The `draiver canary`
command (this ticket) is the reusable measurement tool. The one remaining step —
executing the live flag-on/off run, which spends real subscription budget and
reconfigures the daemon — is gated on human authorization (see "Status" at the
end).

## What we are proving

Two axes, from `docs/prompt-caching.md` §"Measuring the benefit":

1. **The cache win is real and cross-ticket.** A second attempt on a repo should
   turn-1 *read* the shared tools+system+append prefix instead of re-creating it,
   and that should show up as lower cache-normalised work per attempt.
2. **Outcome parity — the regression gate.** `--exclude-dynamic-system-prompt-sections`
   relocates the working-directory / git-state / platform context out of the
   system prompt into the first user message. The Agent-SDK docs warn Claude "may
   rely on [it] less strongly." So the gate is behavioural: with the flag on,
   agents must still resolve **cwd, git state, and file paths** correctly. This is
   a regression gate, not merely a cost measurement.

## The two arms

| Arm | `~/.draiver/config.json` keys | Effect |
| --- | --- | --- |
| **off** (control) | *(unset — both keys absent)* | launches byte-for-byte as today |
| **on** (under test) | `"exclude_dynamic_system_prompt_sections": true`, `"append_system_prompt_file": "<invariant-protocol>.txt"` | strips the dynamic system-prompt sections (rung D) and hoists the invariant protocol into the static append (rung E) |

Both arms already run with `ENABLE_PROMPT_CACHING_1H=1` (set on `BaseSpec.Env` by
`newReconciler`, drvctl-035), so TTL is held at 1h in either arm.

**Preconditions:**

- The pinned `claude` must accept `--exclude-dynamic-system-prompt-sections` — the
  daemon fails loud at start-up otherwise (`resolvePromptCache` in `cmd/ctl.go`).
- The append file must be **byte-invariant** — no ticket id, no timestamp, nothing
  per-ticket — or the shared prefix re-fragments (`docs/prompt-caching.md`
  "Constraint — the append must be byte-invariant").
- Pin the adapter version for the run so a mid-run Claude Code upgrade does not
  bust the prefix fleet-wide. **Caveat:** `drvctl-033` (version pin + version
  events) is *not yet implemented*, so this must be enforced manually for now
  (`DISABLE_AUTOUPDATER=1`, fixed `claude` binary) — see "Unmet dependencies".

## The three assertions and how each is measured

Run the **same** representative tickets through each arm. LLM runs are stochastic,
so use **N runs per arm** (N ≥ 3) and average; cache warmth you control, generation
variance you can only average out.

### (a) Turn-1 `cache_read` appears on the *second* ticket of a repo

The direct sharing test. Order matters: the second ticket must cold-start on the
same repo *after* the first, within the 1h TTL, so the shared prefix is still warm.

- **Measure:** `draiver canary` — the per-repo rollup shows each attempt's
  `t1-read` (turn-1 `cache_read`), `t1-share` (turn-1 read share), and marks a
  non-baseline attempt `COLD` if it failed to reuse the prefix.
- **Pass (arm on):** the second+ attempts show `t1-read` ≈ the prefix size and
  `verdict=ok` (not `COLD`).
- The turn-1 numbers come from each session's `stream.jsonl` first usage frame; the
  aggregate `meter.json` cannot show this (within-session caching swamps it — see
  `docs/prompt-caching.md` and the canary's package doc).

### (b) The cost win shows up in the cross-attempt rollup

Compare on **cache-normalised work**, not billed cost (billed mixes in cache luck;
`docs/prompt-caching.md` §"Measuring the benefit").

- **Measure:** `draiver canary` rollup columns `norm-work`
  (`input + cache_creation + cache_read + output`, all 1×) and `billed-in`
  (input×1 + creation×1.25 + read×0.1). Compare the per-attempt distribution
  on-arm vs off-arm for the same tickets.
- **Pass:** on the *second+* attempts of a repo, the **on** arm shows lower
  cache-normalised cold-write work than **off** (the prefix is read, not
  re-created). Billed-input should drop too, but is reported for reference, not
  compared on.
- `drvweb-013` (the web rollup + A/B cohorts) is not built; `draiver canary` is the
  CLI stand-in that reads the same drvctl-031 metrics.

### (c) Outcome / behaviour parity — the regression gate

- **Measure:** for each representative ticket, compare terminal control state
  across arms (did the attempt reach `Review`/`Done`, or error / stall on a
  cwd/git/path mistake?) **and** spot-check the agent's actual transcript for the
  three failure modes the relocation could introduce:
  1. **cwd** — did the agent run in the correct worktree directory?
  2. **git state** — did it read the right branch / base / status?
  3. **file paths** — did it edit the intended files (not a sibling worktree)?
- **Pass:** the **on** arm resolves all three as reliably as **off** across the N
  runs — no new class of cwd/git/path error attributable to the stripped sections.
- This axis is partly manual (behavioural), by design: it is the gate, so a human
  reads the sample rather than trusting a single scalar.

## Run protocol (turnkey once authorized)

1. Pick **representative tickets** — a mix of shapes (a small edit, a
   multi-file change, one that reads git history) on **one repo**, so second-ticket
   sharing is exercised.
2. Pin the adapter (`DISABLE_AUTOUPDATER=1`, fixed binary) for the whole run.
3. **Off arm:** ensure both config keys are unset; run each ticket N times,
   letting the daemon bring the sessions up.
4. **On arm:** set the two config keys; point `append_system_prompt_file` at the
   byte-invariant protocol; restart the daemon; run the same tickets N times **in
   the same per-repo order** (so ticket 2 follows ticket 1 within TTL).
5. **Read the verdict:** `draiver canary` for (a) and (b); the transcript sample +
   terminal states for (c). Fill the table below.

## Verdict template

Fill after the run (numbers per arm, averaged over N):

| Metric | off (control) | on (under test) | Assertion |
| --- | --- | --- | --- |
| ticket-2 turn-1 `cache_read` | _tbd_ | _tbd_ | (a) on ≫ 0, `verdict=ok` |
| ticket-2 turn-1 read-share | _tbd_ | _tbd_ | (a) on ≥ floor |
| ticket-2 cache-normalised work | _tbd_ | _tbd_ | (b) on < off |
| ticket-2 billed-input (ref) | _tbd_ | _tbd_ | (b) reference only |
| reached Review/Done (of N) | _tbd_ | _tbd_ | (c) parity |
| cwd / git / path errors | _tbd_ | _tbd_ | (c) no new errors on-arm |

**Conclusion:** _tbd — flags safe to default? yes/no, with the numbers above._

## The standing guard: `draiver canary`

After the one-off validation, the silent-invalidator **canary** is the ongoing
gate (acceptance criterion 2). It scans every repo's attempts and fires
(exit `5`) when the second+ attempts stop reusing the shared prefix — the
signature of a bust:

- **per-ticket data leaked into the append** — turn-1 re-creates the prefix, no
  adapter-version change ⇒ finding points at the append.
- **an unpinned adapter upgrade** — turn-1 re-creates the prefix *and* the adapter
  version changed ⇒ finding points at the version bump (this attribution lights up
  once `drvctl-033` records version events; today it reports "version
  unavailable").
- **a sub-minimum / hard invalidator** — caching never engaged at all
  (`caching_active=false`).

Run it in CI/cron against the data root; a non-zero exit means the cross-ticket
cache win has silently regressed. Its correctness is proven by the
`internal/canary` tests (fires on an intentionally-busted fixture, quiet on a
healthy one) — acceptance criterion 2.

## Unmet dependencies (read before running)

Two tickets this capstone lists as dependencies are **not implemented** (roadmap
only in `docs/prompt-caching.md`; confirmed by `git log --all`):

- **`drvctl-033`** (pin adapter version + gate autoupdate, *and* the version
  events). Consequence: the version pin must be enforced manually for the run, and
  the canary cannot yet *attribute* a bust to an adapter upgrade — it detects the
  bust but reports the version as unavailable. The attribution seam
  (`canary.Observation.AdapterVersion`) is in place and lights up when 033 lands.
- **`drvweb-013`** (cross-attempt / per-repo web rollup + A/B cohorts). Consequence:
  assertion (b) reads the `draiver canary` CLI rollup instead of a web rollup.

## Status

- **Canary (criterion 2): done** — `internal/canary` + `draiver canary`, tested,
  and demonstrated quiet on the live (healthy) fleet.
- **Validation run (criterion 1): mechanism ready, execution gated.** Executing the
  live run spends real subscription budget and reconfigures the running daemon, and
  depends on the two unmet tickets above. That authorization + parameters
  (representative tickets, N, whether to build 033/drvweb-013 first) is an open
  escalation on drvctl-036.
