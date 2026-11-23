---
section: Added
---
**Human decisions, resolutions and supersedes now reach a running session as a turn, and `review` waits until the agent has acknowledged them (drvctl-062).** Before this, a session saw human input only if it happened to run a `draiver` command, and only if the input was explicitly addressed to it. In drv-044 the human retracted feedback five minutes after posting it; the session never saw the retraction and built the wrong feature.

- **Deliverable events.** The drvctl-059 message channel now also covers any human `decision` or `resolution`, and any human event that supersedes an earlier fact, whether or not it is marked "Message the agent". A human `note` counts only when marked, because daemon bookkeeping notes are stamped `human:*` too. The agent's own events never count.
- **Push.** Each reconcile tick injects new deliverable events into the live session as one short turn quoting their text (capped), e.g. `#15 supersede (retires #14)`. The turn is queued for the agent's next turn boundary. Pushing does not mark anything read.
- **Writes name what's unread.** Inside a session, `draiver log` and `draiver review` print a second line under the `📬` count: `⚠️ N new human events since your last read: #15 supersede (retires #14); …`. It lists seqs and kinds only, never content.
- **Acknowledgement.** `review` still refuses (exit 8) while human events are unread. To acknowledge them, run `draiver messages`, or use the new `review --ack`, which prints them in full, marks them read and records the claim. `--ignore-unread` still claims without reading.
- **Resume note.** The resume note marks human events the previous session never read and names what each supersede retires. It lists at most the newest 10 events and counts the rest.
