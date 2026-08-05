package web

import (
	"encoding/json"
	"errors"
	"os"
	"syscall"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/session"
)

// dotVM drives the board's session-liveness dot: a small coloured circle in a
// card's meta-row encoding whether an agent process is running for the attempt
// right now. An empty State renders no dot.
type dotVM struct {
	State string // "running" | "stopped" | "disabled"; "" = no dot
	Label string // title / aria-label text
}

// sessionDot computes an attempt's runtime-liveness dot from its session.json and
// a live pid probe. The enum, first match wins:
//
//	none      — no session.json or empty session_id: a fresh, never-run attempt
//	            shows nothing rather than a misleading grey.
//	running   — the recorded pid is alive (eucalypt green). Liveness wins even if
//	            the attempt is disabled: the dot reflects reality and self-corrects
//	            when the daemon reaps it (spec decision #2).
//	none      — no live pid but the attempt is Stuck: the rust-red Stuck signals
//	            (column + badge + favicon) already carry it, so we suppress the dot
//	            rather than paint it orange, so "stopped" never reads as "stuck"
//	            (spec decision #1).
//	disabled  — no live pid and not enabled (ghost-gum grey): an agent used to run
//	            here but the attempt is disabled.
//	stopped   — no live pid and enabled (wattle gold): stopped, not stuck.
//
// pid-reuse is an accepted Tier-0 risk (a recycled pid could make a stopped
// session look running), the same tradeoff reconcile.OSProc.Alive documents; it
// is low risk for a local dev dashboard (spec decision #3).
func (s *Server) sessionDot(a project.Attempt) dotVM {
	id, ok := s.readSessionIdentity(a.Ticket, a.ID)
	if !ok || id.SessionID == "" {
		return dotVM{}
	}
	if id.PID != 0 && s.alive(id.PID) {
		return dotVM{State: "running", Label: "agent running"}
	}
	if a.State == project.NeedsMe {
		return dotVM{}
	}
	if !a.Enabled {
		return dotVM{State: "disabled", Label: "disabled"}
	}
	return dotVM{State: "stopped", Label: "stopped"}
}

// readSessionIdentity reads an attempt's session.json directly, without creating
// anything. It deliberately does NOT go through session.Open, which would
// MkdirAll the session/ dir — the web server is strictly read-only and must not
// materialise a session dir for every never-run attempt just to check liveness.
// A missing/unparseable file (never ran) reads as "no session".
func (s *Server) readSessionIdentity(ticket, attempt string) (session.Identity, bool) {
	data, err := os.ReadFile(s.root.SessionMetaPath(ticket, attempt))
	if err != nil {
		return session.Identity{}, false
	}
	var id session.Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return session.Identity{}, false
	}
	return id, true
}

// pidAlive probes whether pid names a live process with a signal-0 check — the
// kernel's existence-and-permission test, delivering no signal. This mirrors
// reconcile.OSProc.Alive: the web server is a separate read-only process, so it
// can't ask the daemon and must probe the OS process table itself. ESRCH means
// gone; EPERM means alive but owned by another user.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
