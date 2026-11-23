---
section: Added
---
**Copy a "fork this session" command from the attempt page (drvweb-029).** A `claude-code`
attempt with a session id now shows a **Copy fork command** button on the right of the
attempt page's Agent Logs header. It puts `cd '<worktree>' && claude --resume '<session_id>' --fork-session
--permission-mode plan` on the clipboard, and the command is also shown in a selectable
`<code>` inside the Agent Logs panel. An operator can use it to question the agent in a private fork. The fork has a
new session id, so nothing reaches the draiver log or the transcript draiver resumes, and
it runs in read-only plan mode because it shares the worktree. The command is built
server-side from `session.json` with both values shell-quoted, and no button appears
without a session id, an absolute worktree, or the `claude-code` adapter. Copying falls
back from `navigator.clipboard` to `execCommand('copy')` for boards served over plain http.
Rendering it writes no log event.
