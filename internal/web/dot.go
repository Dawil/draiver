package web

import (
	"bytes"
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
	State string // "running" | "error" | "stopped" | "disabled"; "" = no dot
	Label string // title / aria-label text
}

// healthStart is ctl.jsonl's "an error class began affecting this attempt" event
// kind (reconcile.healthStart, "start"); a class whose most-recent line is this is
// still erroring. Mirrored here as a literal — rather than importing the daemon's
// reconcile package into the strictly read-only web server — the same decoupling
// pidAlive makes when it mirrors reconcile.OSProc.Alive. dot_test.go marshals a
// real reconcile.HealthEvent to guard this and the JSON field names against drift.
const healthStart = "start"

// healthLine is the minimal projection of a session/ctl.jsonl line the dot needs:
// which error class, and whether this transition opened ("start") or closed it. It
// mirrors the wire shape of reconcile.HealthEvent (the shared drvctl-027 contract);
// ts/message are ignored here.
type healthLine struct {
	Class string `json:"class"`
	Event string `json:"event"`
}

// sessionDot computes an attempt's runtime-liveness dot from its session.json, a
// live pid probe, and the daemon's session/ctl.jsonl health log. The enum, first
// match wins:
//
//	running   — the recorded pid is alive (eucalypt green). Liveness is ground
//	            truth and wins over everything, even a still-open error class (a
//	            live process and "can't cut the worktree" are mutually exclusive;
//	            if ctl.jsonl ever lags into that impossible pair, the running pid
//	            is believed). It also wins even if the attempt is disabled: the dot
//	            reflects reality and self-corrects when the daemon reaps it (spec
//	            decision #2).
//	error     — no live pid but draiverctld currently can't run this attempt: some
//	            health error class is still open in ctl.jsonl (waratah crimson).
//	            This is checked BEFORE the empty-session_id/no-session.json returns
//	            below, because the wedge this dot exists to expose sat at
//	            session_id="" — the very dotVM{} "no dot at all" case (drvweb-008
//	            gotcha #2). It outranks stopped/disabled/none: the supervisor being
//	            unable to run the attempt is the most urgent thing to surface.
//	none      — no live pid, no open error, and no session.json or empty
//	            session_id: a fresh, never-run attempt shows nothing rather than a
//	            misleading grey.
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
	if ok && id.PID != 0 && s.alive(id.PID) {
		return dotVM{State: "running", Label: "agent running"}
	}
	if s.sessionErroring(a.Ticket, a.ID) {
		return dotVM{State: "error", Label: "draiverctld cannot run this attempt"}
	}
	if !ok || id.SessionID == "" {
		return dotVM{}
	}
	if a.State == project.NeedsMe {
		return dotVM{}
	}
	if !a.Enabled {
		return dotVM{State: "disabled", Label: "disabled"}
	}
	return dotVM{State: "stopped", Label: "stopped"}
}

// sessionErroring reports whether draiverctld currently cannot run the attempt —
// whether any daemon health error class is still open in session/ctl.jsonl.
//
// ctl.jsonl (drvctl-027) is an append-only, edge-triggered log: one line when an
// error class starts affecting the attempt ("start"), one when it clears ("end").
// So the rule is just most-recent-wins per class — a class whose latest line is a
// "start" is still erroring — with no counting or thresholds. It is deliberately
// class-agnostic: ANY open class lights the dot (a wedged worktree, the
// admit-failed catch-all that covers the corrupt-object variant, ...), so no real
// wedge renders as nothing (gotcha #2). A missing file — older data, or an attempt
// that never tripped — means "no error": today's behaviour, unchanged.
//
// Read directly with os.ReadFile, like readSessionIdentity and pidAlive: the web
// server is out-of-process and read-only, so it parses the daemon's log itself
// rather than asking it.
func (s *Server) sessionErroring(ticket, attempt string) bool {
	data, err := os.ReadFile(s.root.SessionCtlLogPath(ticket, attempt))
	if err != nil {
		return false
	}
	open := map[string]bool{} // class -> is its most-recent transition a start?
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev healthLine
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // tolerate a torn trailing write; the next tick re-emits
		}
		if ev.Class == "" {
			continue
		}
		open[ev.Class] = ev.Event == healthStart
	}
	for _, erroring := range open {
		if erroring {
			return true
		}
	}
	return false
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
