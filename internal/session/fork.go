package session

import (
	"errors"
	"strings"
)

// ForkAdapter is the only coding-agent adapter whose CLI can fork a session
// (`claude --resume <id> --fork-session`), drvweb-029.
const ForkAdapter = "claude-code"

// ForkCheck reports whether an operator can fork this session: it must be a
// claude-code session with a non-blank session id. It is the one gate shared by
// the board's copy button and `draiver fork`, so the two cannot disagree about
// which attempts are forkable. It returns nil when forkable, else the reason.
func ForkCheck(id Identity) error {
	if id.Adapter != ForkAdapter {
		if id.Adapter == "" {
			return errors.New("session has no adapter; only claude-code sessions can be forked")
		}
		return errors.New("session adapter is " + id.Adapter + "; only claude-code sessions can be forked")
	}
	if strings.TrimSpace(id.SessionID) == "" {
		return errors.New("session has no session id yet; nothing to fork")
	}
	return nil
}

// ForkArgv is the claude invocation that forks session sid: a NEW session id
// (--fork-session) so the original transcript draiver resumes is never written
// to, in plan mode so the fork cannot edit the worktree it shares with the
// agent. It must run with cwd = the session's worktree, because Claude Code keys
// transcripts by cwd.
func ForkArgv(sid string) []string {
	return []string{"claude", "--resume", strings.TrimSpace(sid), "--fork-session", "--permission-mode", "plan"}
}
