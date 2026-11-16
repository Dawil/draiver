---
section: Added
---
**`ctl merge --remote[=NAME]` — close a ticket landed by an external PR
(drvctl-029).** The increasingly common land path is not `ctl merge` but a PR
merged on the forge; afterwards the code is in the *remote* base and draiver had
no verb for it, leaving the user to hand-run `git fetch` + a fast-forward and then
`done`. `--remote` is the external twin of `ctl merge`: instead of landing the
local branch it (1) `git fetch`es the attempt's recorded `base:` from the remote —
the **only** place draiver reaches a remote, and it **never pushes**; (2) verifies
the branch tip is contained in the fetched `refs/remotes/<remote>/<base>`, the
proof the PR really merged (if not, it refuses and records nothing, taking the
same `--escalate`/`--no-escalate` disposition as the other land verbs); (3)
records `done` so control state follows reality — the code is in the base, just
the remote base this time; and (4) **best-effort** fast-forwards the local base to
the fetched ref. Step 4 is hygiene, never load-bearing: a missing, dirty, or
diverged local base is a printed **warning**, never un-closing the ticket the
containment gate already proved landed. It is gated to a stopped `Review` attempt
(still the Review → Done transition) but, unlike local `ctl merge`, does **not**
require a clean base checkout — a dirty base just skips the pull. `NAME` defaults
like `review`'s rule: exactly one remote → use it; several remotes → the config's
`primary_remote`, else refuse asking which. draiver still never deletes the branch
or talks to the forge API. This is the verb the webui's "Merged elsewhere — close
& pull" button (drvweb-010) shells out to.
