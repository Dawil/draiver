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

**Important — the append (rung E) is already default-on.** `drvctl-034` made
`resolvePromptCache` default `SystemPromptAppend` to `handbook.Content()` (the
byte-invariant protocol, embedded via `go:embed`). `append_system_prompt_file` only
*overrides* that default; an empty override file disables it. So both arms already
carry the invariant-protocol append — the **sole lever under test** is
`exclude_dynamic_system_prompt_sections` (drvctl-034 left it default-off as the
opt-in cache lever).

| Arm | `~/.draiver/config.json` keys | Effect |
| --- | --- | --- |
| **off** (control) | *(unset — `exclude_dynamic_system_prompt_sections` absent)* | append default-on (handbook); dynamic system-prompt sections retained — launches as today |
| **on** (under test) | `"exclude_dynamic_system_prompt_sections": true` | append default-on (handbook, unchanged); strips the dynamic system-prompt sections (rung D), relocating cwd / git-state / platform into the first user message |

`append_system_prompt_file` is optional in both arms: leave it unset to use the
default handbook append (recommended — it is the canonical byte-invariant protocol),
or point it at your own byte-invariant file to override.

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

## Run protocol (turnkey — authorized on drvctl-036, escalation #8)

The daemon is a foreground process (`draiver ctl up`, the operator's terminal), so
the **operator drives the config flip + restart**; everything else is measured by
`draiver canary`. Representative tickets: **`drvctl-033`** (reads git history / small
config change) and **`drvweb-013`** (multi-file web change) on the **draiver** repo —
two shapes on one repo, so second-ticket sharing is exercised. Working them also
lands the two roadmap dependencies (see "Unmet dependencies").

Preconditions verified this session: `claude 2.1.216` accepts
`--exclude-dynamic-system-prompt-sections` (start-up probe passes); `draiver canary`
installed (`go install .`).

```sh
# --- OFF arm (control) is ALREADY on disk ---
# 47 recorded attempts are all flag-off (see baseline table below). No new off-arm
# runs are strictly required; to run drvctl-033/drvweb-013 off-arm for a same-ticket
# pair, do so BEFORE flipping the key, same steps as the on arm minus the config edit.

# --- ON arm ---
# 1. Pin the adapter for the run (belt-and-suspenders; autoupdater unlikely mid-run).
export DISABLE_AUTOUPDATER=1

# 2. Flip the sole lever in ~/.draiver/config.json (append stays default-on):
#    add  "exclude_dynamic_system_prompt_sections": true

# 3. Restart the daemon so it re-reads config (it re-adopts running sessions):
#    Ctrl-C the foreground `draiver ctl up`, then relaunch it (same flags).

# 4. Run the tickets N times each, in per-repo order (ticket 2 within the 1h TTL of
#    ticket 1) so the shared prefix is warm. For each new run:
draiver attempt new drvctl-033        # -> 000X ; then:
draiver ctl enable  drvctl-033@000X
draiver ctl start   drvctl-033@000X   # daemon spawns it in the background
#    ...repeat for drvweb-013 and for N total (spec: N>=3).

# 5. Read the verdict once the attempts retire:
draiver canary --repo /home/david/Projects/draiver   # (a) + (b): rollup + exit code
draiver ctl logs <ticket>@000X                        # (c): read cwd/git/path behaviour
```

Then fill the verdict table below (on-arm column) and compare against the OFF-arm
baseline already recorded there.

## Verdict template

The **off (control) column is measured** — from the 47 recorded flag-off attempts
(`draiver canary`, this session; `main` already carries the drvctl-034 handbook
append, so these off-arm attempts already include rung E). The **on column** is
filled after the operator runs the on arm above (numbers per arm, averaged over N).

Measured on-arm over 6 attempts — `drvctl-033@0002–0004` + `drvweb-013@0002–0004`,
two ticket shapes on the **draiver** repo under `exclude_dynamic_system_prompt_sections:
true` (`draiver canary --repo /home/david/Projects/draiver`). The fair off-column
comparison is the **append-era** off attempts (`drvctl-032@0001 … drvweb-014@0001`),
which already carry the same `drvctl-034` handbook append, so the *only* difference is
the flag.

| Metric | off (control) — measured | on (measured, N=6) | Assertion |
| --- | --- | --- | --- |
| ticket-2 turn-1 `cache_read` | 16,584–16,876 (append-era ~16,834 avg) | **17,646** (all 6, identical) | (a) **PASS** — on ≫ 0, `verdict=ok` |
| ticket-2 turn-1 read-share | ~0.69 avg (0.66–0.74) | **0.77** (all 6) | (a) **PASS** — on well above 0.5 floor |
| ticket-2 turn-1 cold-write (`t1-create`) | append-era ~6,378 avg (fleet ~7,200) | **5,271 avg** (5,128–5,310) | (b) **PASS** — −17% vs append-era off |
| turn-1 `verdict` | `ok`, canary exit 0 | **`ok`, canary exit 0** | (c)+(a) canary quiet |
| init `cwd` = own worktree (of 6) | baseline: correct | **6 / 6** | (c) **PASS** |
| git-state probes resolved (of 6) | baseline: correct | **6 / 6** (4–15 probes each) | (c) **PASS** |
| sibling-worktree path leakage | baseline: none | **0 / 6** | (c) **PASS** |

**Off-arm baseline notes (measured this session):** across all three repos every
non-baseline attempt reads the full shared prefix on turn 1 (`t1-read`
16,584–16,876 ≈ the machine-warm tools+system+append prefix), `t1-share` 0.66–0.74,
`hit-ratio` ≥ 0.91, and `verdict=ok` — `draiver canary` exits `0` (healthy). This is
the control the on arm must **match or beat** on cache metrics (a)/(b) **without**
regressing behaviour (c). Because the append is already default-on, the on arm's
expected delta is a *larger, more stable* cached turn-1 prefix (the dynamic sections
no longer sit inside it to be re-created per attempt) and correspondingly lower
cold-write (`t1-create`) — not the appearance of caching from nothing.

**Conclusion — the flags are safe to default, and are now the default.**
`exclude_dynamic_system_prompt_sections: true` delivers the predicted win — a
**larger** cached turn-1 prefix (+812 tokens: the cacheable boundary now extends past
where cwd/git/platform used to fragment it), **−17%** turn-1 cold-write, read-share
**0.69 → 0.77** — with **zero** behavioural regression: all 6 on-arm attempts resolved
cwd, git state, and file paths correctly (6/6 on every parity check), and the canary
stayed quiet (`verdict=ok`, exit 0). The flag is live in `~/.draiver/config.json`
(set by the operator, drvctl-036 escalation #12/#19) and authorized permanent (#19).

## Cache-hit reality — what the ceiling actually is

The success metric was framed as "cache hit rate above 100%." A cache **hit ratio is
bounded at 100%** — it is the fraction of input tokens served from cache
(`cache_read / (cache_read + cache_creation + input)`), so it can never exceed 1.0.
It is not a throughput multiplier. Where the fleet actually sits:

- **Whole-session hit ratio is already ~0.95–1.00** across both arms — within-session
  turn-to-turn caching dominates once a conversation is long, so this number is near
  its ceiling regardless of the flag. (The 6 on-arm attempts average 0.968; the slight
  spread below 1.0 tracks *conversation size*, not the flag — these attempts did
  millions of tokens of real work, growing the prompt and forcing within-session
  cache-creation.)
- **Turn-1 read-share is the flag-sensitive number**, and it moved **0.69 → 0.77**.
  It cannot reach 1.0 by construction: turn 1 always carries a per-ticket *first user
  message* (the brief + the relocated cwd/git/platform context) that has never been
  seen before, so it must be written (`cache_creation`), never read. The ~17.6K read
  prefix is the byte-invariant tools + system + handbook append; the remaining ~23% is
  the irreducible per-ticket payload.

So "above 100%" is unreachable — but the useful levers are real, and the flag pulled
them the right way.

### What could push cache efficiency further

1. **Land `drvctl-033` (version pin + version events).** Today the autoupdater is only
   held off manually (`DISABLE_AUTOUPDATER=1`). An unpinned `claude` upgrade mid-fleet
   silently rewrites the tools/system prefix and busts *every* repo's shared cache at
   once — the highest-leverage remaining risk. Landing 033 also lets the canary
   *attribute* a bust to a version bump instead of reporting "version unavailable".
2. **Shrink the per-ticket first user message.** The turn-1 cold-write floor (~5.3K) is
   dominated by the brief + relocated dynamic context. Any stable scaffolding still
   living in that message could move into the byte-invariant append (which *is* cached);
   only the genuinely per-ticket bytes need to stay uncacheable.
3. **Keep the append strictly byte-invariant.** A single per-ticket byte (an id, a
   timestamp) in the append re-fragments the shared prefix and erases the win. The
   `draiver canary` guard exists precisely to catch this — run it in CI/cron.
4. **Order same-repo attempts within the 1h TTL.** The win is cross-ticket prefix
   *reuse*; a second attempt that cold-starts after the prefix has aged out of the 1h
   window re-creates it. Scheduling repo-adjacent work close in time preserves warmth.
5. **`drvweb-013` for visibility (UI only).** Not a cache lever, but surfacing the
   per-repo rollup in the web UI (currently CLI-only via `draiver canary`) makes silent
   regressions visible without a cron alert.

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
  installed fleet-wide, and demonstrated quiet on the live (healthy) fleet
  (exit `0`, all 40 reuse attempts `verdict=ok`).
- **Validation run (criterion 1): DONE — both arms measured, gate passed.**
  Escalation #8 authorized the spend and chose the reduced-validation path (skip
  building drvctl-033's version pin; read the rollup from `draiver canary` in lieu
  of the unbuilt drvweb-013 web rollup). The operator flipped the key and restarted
  the daemon (#12); the on arm ran 6 attempts (`drvctl-033@0002–0004`,
  `drvweb-013@0002–0004`) on the draiver repo. All three assertions pass (verdict
  table above): (a) turn-1 prefix read on every reuse attempt, (b) −17% cold-write
  vs the append-era off control, (c) 6/6 clean on cwd, git-state, and file-path
  parity with zero sibling-worktree leakage. Human authorized the flags permanent
  (#19); the config key is live in `~/.draiver/config.json`.
  - **Follow-up (not blocking):** the flag is permanent via the config key, but the
    *code* default in `internal/config/config.go` (`ExcludeDynamicSystemPromptSections`)
    is still the zero-value `false`. Baking the default to `true` there — so the flag
    is on even absent the config key — is a one-line follow-up that belongs to the
    drvctl-032 config surface, left out of this validation ticket to avoid scope creep.
