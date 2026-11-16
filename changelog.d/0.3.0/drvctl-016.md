---
section: Changed
---
**Session lifecycle verbs recast around a state-stack "degree axis", and the
imperative verbs now hand off to the daemon (`draiverctl`, drvctl-016).**
`start`/`stop`/`restart` were an ad-hoc foreground driver: the resume path blindly
`--resume`'d any recorded session id — so an id that could no longer be resumed
was retried forever with no exit but a manual `rm -rf …/session/` — while
`restart` resumed the same id yet force-fed a fresh brief, neither a clean
continue nor a clean fresh start. Bring-up is now one **self-heal cascade** that
climbs from the highest surviving layer: a recorded session is Resumed and
**confirmed online** (the first `system`/init frame, via a new adapter-agnostic
`agent.Onliner` seam); an id that no longer resumes has its dead process reaped
and falls through to a fresh `Spawn` on the same worktree. **Brief-on-reset** is
coupled to that: a fresh spawn is cold-started from the brief, a resume continues
without a re-brief — so the "restart re-briefs a resume" incoherence is gone.
