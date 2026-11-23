package web

import (
	"path/filepath"
	"strings"

	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
)

// shellQuote single-quotes s for a POSIX shell: the whole value is wrapped in '…'
// and every embedded ' becomes '\” (close quote, escaped quote, reopen), so a
// value with spaces, quotes, $ or backticks pastes as exactly one literal word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// forkCommand builds the ready-to-paste "fork this session" command (drvweb-029)
// the board's copy button carries:
//
//	draiver [--data '<root>'] fork '<ticket>@<attempt>'
//
// `draiver fork` (cmd/fork.go) cds into the session's worktree, recreating it if
// the daemon reclaimed it, and execs `claude --resume <id> --fork-session
// --permission-mode plan`. That replaced a raw `cd … && claude --resume`
// one-liner, which broke once a retired attempt's clean worktree was removed
// (decision #24). The fork gets a NEW session id, so the operator's turns never
// reach the transcript draiver resumes; plan mode keeps it read-only.
//
// dataDir is the webui's data root. It is passed as --data only when it differs
// from the CLI default (store.DefaultDir), so the common case pastes short.
//
// It returns "" (no command, so no button) unless session.ForkCheck passes (a
// claude-code session with a session id; the same gate `draiver fork` applies)
// and the worktree is absolute. A relative worktree is not resolved: session.json
// carries no base to resolve it against, and the verb would refuse it anyway.
func forkCommand(id session.Identity, ticket, attempt, dataDir string) string {
	if session.ForkCheck(id) != nil || !filepath.IsAbs(id.Worktree) {
		return ""
	}
	cmd := "draiver "
	if def, err := store.DefaultDir(); dataDir != "" && (err != nil || filepath.Clean(dataDir) != filepath.Clean(def)) {
		cmd += "--data " + shellQuote(dataDir) + " "
	}
	return cmd + "fork " + shellQuote(ticket+"@"+attempt)
}

// forkCommandFor reads an attempt's session.json and returns its fork command,
// or "" when there is no session.json or forkCommand declines it.
func (s *Server) forkCommandFor(ticket, attempt string) string {
	id, ok := s.readSessionIdentity(ticket, attempt)
	if !ok {
		return ""
	}
	return forkCommand(id, ticket, attempt, s.root.Dir)
}
