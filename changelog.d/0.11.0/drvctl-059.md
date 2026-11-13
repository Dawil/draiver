---
section: Added
---
**Agent unread-message notice — human messages reach a live session
(drvctl-059).** A message the human posted while an agent session was already
running was never delivered (prompts only fire at cold-start and resume), and
once the agent claimed review it was lost for good. The board compose form now
marks its entries as messages to the agent (a default-checked **📬 Message the
agent** box; the Review "Decision — reopen" button marks too; marked entries
show a `📬 → agent` badge) via a new omitempty `to: agent` event field — actor is
never used, since spine notes are stamped `human:*` too. `draiver log --to agent`
does the same from a terminal. draiverctld hands each session its own
`DRAIVER_SESSION_TICKET/ATTEMPT/SEQ` (the log tail at bring-up), and every
agent-facing verb (`log`, `test`, `review`, `recall`, `brief`, `park`,
`escalate`, `status`) prints a count-only `📬 N unread message(s)…` notice on
stderr when marked messages landed above that watermark and the session's read
cursor (`test` repeats it at the end). The new **`draiver messages`** verb prints
them in full and advances the cursor (`session/messages-read.json`, off-chain).
**`draiver review` is soft-stopped** with unread messages — no event, exit 8,
and a hint — unless they are read or `--ignore-unread` is passed. The resume
note now also lists a message posted during the previous session and never
read, even when the review sits above it (the exap-023 #12 case). Outside a
session nothing changes, and any error computing the notice is silent.
