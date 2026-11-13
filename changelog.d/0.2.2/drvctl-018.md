---
section: Changed
---
**`ctl status` hides Done attempts by default; `--all`/`-a` includes them
(`draiverctl`, drvctl-018).** The live view printed one row per attempt,
terminal Done ones included, so as closed tickets pile up they drown the
Running/Stuck/Review attempts an operator actually cares about. The default
list now shows only non-Done attempts; `--all`/`-a` restores the full list, and
an explicitly named target (`status <ticket[@attempt]>`) still prints its match
regardless of state. When the default filters everything away, a one-line hint
reports the hidden Done count and the `--all` opt-in instead of blank output.
Scope is `ctl status` only; the web board (`draiver status`) is untouched.
