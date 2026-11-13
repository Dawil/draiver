---
section: Changed
---
**`--repo` is required at attempt creation; a repo-less admit escalates instead
of silently stalling (`draiverctl`, drvctl-017).** Two gaps around a missing
repo, closed at both ends. **At creation:** `attempt.Create` trims and rejects
an empty repo — the one chokepoint every creation path funnels through — so no
path can mint a repo-less attempt; `new` and `attempt new` validate up front
(before any side effect, no orphan dir), while `attempt new --from` still
inherits its parent's repo (drvctl-015), so `--repo` is required only when it
can't be inherited. **At admit:** a repo-less admit now appends an `escalation`
event, flipping the attempt to Needs-me so it lands on the board with an
actionable ask, rather than staying Running+enabled and stalling silently in the
operational log. The tick never fails as a whole — siblings keep admitting — and
every other admit failure stays log-only.
