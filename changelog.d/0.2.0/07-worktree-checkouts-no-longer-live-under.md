---
section: Fixed
---
**Worktree checkouts no longer live under `.git` — they stalled the agent's
auto-mode classifier (`draiverctl` Tier 0).** The managed base defaulted to
`<git-common-dir>/draiver/worktrees/...`, putting every attempt's checkout —
the agent's working directory — *under* `.git`. A coding agent's auto-mode
permission classifier treats any path under `.git` as protected and refuses to
write there, so bringing up an attempt's isolated workspace during `ctl start`
stalled. `worktree.NewManager` now derives the default base *outside* the
repository — `<user-cache-dir>/draiver/worktrees/<repo-label>-<hash>` — keyed by
a hash of the git-common-dir so it stays deterministic per repo and the
stateless Manager re-computes it after a restart. This preserves the original
goal (checkouts stay out of the tracked working tree — no `.gitignore`, no
accidental commits) while also keeping them out of `.git`; the fix relocates the
workspace rather than touching any gate, so genuine writes into a real `.git`
are still classified as unsafe. The per-attempt branch lives in the repo's refs,
so a checkout swept from the cache is recreated by re-attaching to it
(drvctl-010).
