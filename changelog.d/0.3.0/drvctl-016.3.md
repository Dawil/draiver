---
section: Changed
---
**`start`/`restart`/`enable --now` are imperative-transient control-plane actions
that hand an attempt to a running `ctl up` (`draiverctl`, drvctl-016).** The
self-heal cascade and brief-on-reset coupling now live only in the daemon-shared
bring-up path, so the client verbs cannot drift from the fleet. `ctl up` claims a
**controller pidfile** (`controller.json` — pid, boot nonce, started; removed on
clean exit, liveness-probed so a stale record never passes for a running daemon)
and prints its pid on startup. `start`/`restart` **require a live controller**
(they error if none is up, and `restart` never reaps a session it cannot hand
back), then stamp a per-attempt **desired-marker** with the daemon's boot nonce —
transient by construction, swept when the daemon restarts, so they never leave an
unsupervised orphan; the verb prints a handoff and returns (watch via `ctl logs
-f`). `stop` reaps and removes the marker, leaving the imperative fleet, while a
spent-but-still-desired run is re-admitted by the daemon. `start --new-attempt`
forks a parallel branch and leaves the parent running; `restart --new-attempt`
parks the parent so only the fork runs.
