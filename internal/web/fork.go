package web

import (
	"path/filepath"
	"strings"

	"github.com/Dawil/draiver/internal/session"
)

// forkAdapter is the only coding-agent adapter whose CLI can fork a session
// (`claude --resume <id> --fork-session`). Every other adapter gets no fork button.
const forkAdapter = "claude-code"

// shellQuote single-quotes s for a POSIX shell: the whole value is wrapped in '…'
// and every embedded ' becomes '\” (close quote, escaped quote, reopen), so a
// path with spaces, quotes, $ or backticks pastes as exactly one literal word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// forkCommand builds the ready-to-paste "fork this session" shell command
// (drvweb-029) for a session identity:
//
//	cd '<worktree>' && claude --resume '<session_id>' --fork-session --permission-mode plan
//
// It forks rather than plain-resumes, always: --fork-session opens a copy under a
// NEW session id, so the operator's turns never reach the transcript draiver will
// --resume next, and the fork cannot race a live or later-resumed agent for it.
// --permission-mode plan keeps the fork read-only, because it shares the worktree
// with that agent.
//
// It returns "" (no command, so no button) unless the identity is a claude-code
// session with a non-empty session id and an absolute worktree. A relative
// worktree is not resolved: session.json carries no base to resolve it against,
// and guessing (the webui's cwd) would paste a wrong `cd`. The worktree is
// filepath.Clean-ed so a stray trailing slash or `..` doesn't leak into the paste.
func forkCommand(id session.Identity) string {
	if id.Adapter != forkAdapter {
		return ""
	}
	sid := strings.TrimSpace(id.SessionID)
	if sid == "" || !filepath.IsAbs(id.Worktree) {
		return ""
	}
	return "cd " + shellQuote(filepath.Clean(id.Worktree)) +
		" && claude --resume " + shellQuote(sid) +
		" --fork-session --permission-mode plan"
}

// forkCommandFor reads an attempt's session.json and returns its fork command,
// or "" when there is no session.json or forkCommand declines it.
func (s *Server) forkCommandFor(ticket, attempt string) string {
	id, ok := s.readSessionIdentity(ticket, attempt)
	if !ok {
		return ""
	}
	return forkCommand(id)
}
