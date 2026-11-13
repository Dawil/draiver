---
section: Fixed
---
**Resume actually resumes: the turn is primed before `confirmOnline`
(drvctl-056).** The resume fast-path never worked against headless `claude`. A
bring-up with a recorded session id (every wake after `park`, every restart,
every re-admit from Pending) ran `claude --resume` and then waited up to
`ResumeConfirm` for a `system/init` frame *before* sending any prompt. Headless
`--resume` emits nothing until it receives a user turn, so the confirm always
timed out, the process was killed, and the attempt silently cold-started with a
new session id. Now `bringUp` (`internal/reconcile/bringup.go`) injects the
resume nudge **before** `confirmOnline`, and `drive()` sends the brief only to a
fresh spawn. An id that can't be resumed still times out and falls through to
`Spawn` plus the brief, and the primed turn dies with the killed process, so the
agent is never prompted twice. Resume also requires **context headroom** now: a
session whose `meter.json` context fill is at or above 80% of `ContextLimit`
cold-starts with a clean window instead of resuming straight into an auto-stop.
A missing meter or an unset limit counts as headroom. The resume nudge is
**wake-aware**: `protocol.InjectResumeNudge` takes a wake note. On an async
`test --detach && park` wake, the note names the `test-result` that landed, so
the agent can read the verdict without a `recall` round-trip. The reconcile fake
adapter now emits `init` only after the first input on a resume, matching real
`claude`. Its old unconditional `init` is what hid this bug. A new acceptance
feature (`features/manager-as-worker/resume-on-wake.feature`) covers the
delivered turn.
