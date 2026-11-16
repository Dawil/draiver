---
section: Added
---
**Archive an attempt from its board card — green tick to accept, grey cross
to close (drvweb-019).** Until now nothing could take a card off the board, so it
only grew. Each card now has a hover-revealed control: a **green ✓** on Done
cards (accept and file away) and a **grey ✕** on Running / Stuck / Review cards
(close an attempt that will not finish). Both ask for an `hx-confirm`, then
`POST /ticket/{id}/{attempt}/archive`, and the board region re-renders so the
card disappears and the column counts update in one swap. Archive is a
**reversible flag derived from the log**, mirroring `enable`/`disable`: new
`archive` / `unarchive` event types and `project.DeriveArchived` (last event
wins, default off), exposed as `project.Attempt.Archived`. It is about board
membership only and is independent of the attempt's state; the existing `done`
event is not reused. The sentiment travels in a new `outcome` frontmatter field
(`accepted` | `abandoned`), which the server works out from the attempt's
state. The field is omitempty, so hashes of existing events are unchanged. The
route writes through `ticketlog.Append` directly (there is no CLI verb), is
idempotent and same-origin guarded, and `board()` skips archived attempts. The
cross records board membership only and does not stop a session. There is no
un-archive button yet; an `unarchive` event restores a card.
