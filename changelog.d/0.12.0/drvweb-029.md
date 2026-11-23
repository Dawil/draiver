---
section: Added
---
**`draiver fork`, and a "fork this session" copy button on the attempt page (drvweb-029).**
A `claude-code` attempt with a session id now shows a **Copy fork command** button on the
right of the attempt page's Agent Logs header. It copies `draiver fork '<ticket>@<attempt>'`
(with `--data` when the board serves a non-default data root), and the command is also
shown in a selectable `<code>` inside the Agent Logs panel. The new `draiver fork` verb
changes into the attempt's worktree, recreating it at the same path on the attempt's
branch if the daemon reclaimed it. It then runs `claude --resume <id> --fork-session
--permission-mode plan`, a private, read-only fork under a new session id. Nothing reaches
the draiver log or the transcript draiver resumes, and the fork can't edit the worktree it
shares with the agent. The board and the verb share one gate (`session.ForkCheck`): no
button and no fork without a `claude-code` session id. `draiver fork --print` shows the
directory and command instead of running claude. Copying falls back from
`navigator.clipboard` to `execCommand('copy')` for boards served over plain http. Nothing
here writes a log event. The acceptance rung's browser suite now runs once per unit-test
package instead of three times, which keeps it under `go test`'s 10-minute timeout.
