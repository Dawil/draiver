---
section: Fixed
---
**The board's play button no longer appears on an attempt already running
through a parent edge (drvweb-022).** The green enable button
(`POST /ticket/{id}/{attempt}/enable`) showed up on cards whose agent was
already live because a parent's `wants:`/`after:` edge had activated it. Such a
child never gets a direct `enable` event, but `canEnable` only checked the
durable log bits (`Running && !Enabled`). Clicking the button was not a no-op
either. `handleEnable` loaded the attempt without its runtime bits and wrote a
real `enable` event, which turned an enabled-via-parent child into a directly
enabled attempt whose supervision no longer followed its parent. `canEnable` now
also requires `!Live && !Desired`, so the button follows the effective (Control)
state. `handleEnable` re-derives `Live`/`Desired` with a new `fillRuntime`
helper (the same `DeriveDesired` + `attemptLive` logic as `board()`) before
applying the same check, so a POST for a card that shows no button writes
nothing.
