// health.go is draiverctld's operational-health seam: the piece that makes a
// wedged attempt *observable* without turning it into a human escalation. The
// reconcile loop is level — Tick re-attempts admit every interval — so a stuck
// attempt (its branch checked out in another worktree, a corrupt object at its
// tip, no model to reach) fails silently every tick with nothing surfaced. That is
// the exact failure drvctl-027 exists to kill: drvctl-025/0002 sat Running +
// enabled + desired for a day, admit refused every tick, and nothing anywhere said
// so.
//
// The cure is edge-triggered health transitions. The reconciler holds a per-attempt
// set of currently-active error classes (health, below) — in-memory, rebuildable
// runtime state sitting next to the run table. Each tick reports one observation
// per desired attempt: healthy (admit succeeded, or a session is already live) or a
// single failing class. reportHealth diffs that observation against the active set
// and emits only the *edges* — an `error-start` when a class newly appears, an
// `error-end` when a class that was active clears — appending each to the attempt's
// session/ctl.jsonl. Not one line per failed tick: one line when the trouble
// starts, one when it ends.
//
// The error class is load-bearing: it is the stable identifier the board's red
// error dot (drvweb-008) filters on, so the class strings here are a shared
// contract, not free text. This is deliberately *not* the escalation path
// (escalateNoRepo / errNoRepoBound): those are for genuinely human-actionable
// blocks and flip the attempt to Needs-me. Health is the transient, non-escalating
// sibling — environment trouble that just needs to be seen.
package reconcile

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/worktree"
)

// Health error classes — the stable contract the webui red dot (drvweb-008)
// filters on. They are intentionally coarse: the reader needs to know *what kind*
// of trouble an attempt is in, not the exact git stderr.
const (
	// ClassWorktree is "draiverctld cannot cut the attempt's worktree", spanning
	// every cause git can refuse `worktree add` for — the branch checked out in
	// another worktree (the founding incident), a corrupt/empty object at the tip
	// (gotcha #2), a missing base ref, a pre-occupied path. Keyed deterministically
	// on worktree.ErrCreate so no real variant of the wedge logs nothing.
	ClassWorktree = "worktree-clash"

	// ClassNoNetwork / ClassModelUnreachable are the transient machine-level cases
	// the ticket names: lost connectivity and no API access for the model. They are
	// pinned here so the webui contract (drvweb-008) is stable, but they are *not*
	// emitted from the admit seam: worktree-add and the local process launch are
	// offline operations, so a lost network or unreachable model does not fail admit
	// — it surfaces in the session stream (an EventError frame) instead, a separate
	// health seam out of drvctl-027's scope. Substring-guessing them from an admit
	// error was tried and dropped: bare "401"/"403"/"model" fragments false-match
	// temp paths and hashes, and a wrong class is worse than the honest catch-all.
	// They are reserved for the stream-health seam that classifies those frames.
	ClassNoNetwork        = "no-network"
	ClassModelUnreachable = "model-unreachable"

	// ClassAdmit is the catch-all: any admit failure that is not worktree-clash.
	// Per gotcha #2 this must never be empty-handed — a wedge the specific class
	// misses (a corrupt object surfaced some other way, a manager that cannot open
	// the repo) still logs *something*, so the attempt is never silently stuck.
	ClassAdmit = "admit-failed"
)

// Health event kinds recorded in ctl.jsonl.
const (
	healthStart = "start"
	healthEnd   = "end"
)

// HealthEvent is one line of session/ctl.jsonl: a daemon health transition for an
// attempt. Start marks an error class beginning to affect the attempt; End marks
// it clearing. Message carries the underlying error text on a start (empty on an
// end — the class already says what cleared). The struct is exported because the
// `ctl logs` reader unmarshals it to interleave these transitions with the agent
// stream (drvctl-027).
type HealthEvent struct {
	TS      string `json:"ts"`    // RFC3339 UTC
	Class   string `json:"class"` // one of the Class* contract strings
	Event   string `json:"event"` // "start" | "end"
	Message string `json:"message,omitempty"`
}

// classifyAdmit maps an admit failure to a stable health class. worktree-clash is
// the repro-backed, deterministically-detected class — any `git worktree add`
// failure, recognized via worktree.ErrCreate regardless of the underlying git cause
// (a branch checked out elsewhere, a corrupt object at the tip, a missing base ref;
// gotcha #2). Everything else is the admit-failed catch-all, which — the whole
// point of this ticket — guarantees a real wedge always logs *some* class rather
// than nothing. (The transient network/model classes are not decided here; see
// their doc above.)
func classifyAdmit(err error) string {
	if errors.Is(err, worktree.ErrCreate) {
		return ClassWorktree
	}
	return ClassAdmit
}

// reportHealth folds one tick's observation for an attempt into its active-class
// set and emits the edge transitions. class is the single error class affecting the
// attempt this tick, or "" when it is healthy (admit succeeded or a session is
// already live). A class newly active emits error-start (carrying message); a class
// that was active and is not the one observed this tick emits error-end. A class
// already active and still failing emits nothing — the edge, not the level.
func (r *Reconciler) reportHealth(key worktree.Key, class, message string) {
	r.healthMu.Lock()
	defer r.healthMu.Unlock()

	active := r.health[key]
	// Close every currently-active class that is not the one observed this tick.
	for c := range active {
		if c == class {
			continue
		}
		r.appendHealth(key, HealthEvent{Class: c, Event: healthEnd})
		delete(active, c)
	}
	if class == "" {
		if len(active) == 0 {
			delete(r.health, key)
		}
		return
	}
	if active == nil {
		active = map[string]bool{}
		r.health[key] = active
	}
	if !active[class] {
		r.appendHealth(key, HealthEvent{Class: class, Event: healthStart, Message: message})
		active[class] = true
	}
}

// sweepHealth closes out attempts that dropped out of the desired set since they
// last had an active error class: an attempt that was wedged and is now disabled,
// retired, or blocked is no longer being supervised, so — from the daemon's health
// view — its trouble is over. Emitting the closing error-end resolves the record
// (and the webui red dot) rather than stranding it on a lone error-start forever.
func (r *Reconciler) sweepHealth(desired map[worktree.Key]project.Attempt) {
	r.healthMu.Lock()
	defer r.healthMu.Unlock()
	for key, active := range r.health {
		if _, ok := desired[key]; ok {
			continue
		}
		for c := range active {
			r.appendHealth(key, HealthEvent{Class: c, Event: healthEnd})
		}
		delete(r.health, key)
	}
}

// appendHealth stamps and appends one health event to the attempt's ctl.jsonl. It
// is best-effort — a write failure is logged operationally, never failing the tick;
// ctl.jsonl is rebuildable, and the next transition re-opens the file. Callers hold
// healthMu, which also serializes the append.
func (r *Reconciler) appendHealth(key worktree.Key, ev HealthEvent) {
	ev.TS = r.opt.Now().UTC().Format(time.RFC3339)
	if err := r.opt.Root.EnsureSessionDir(key.Ticket, key.Attempt); err != nil {
		r.opt.Logf("reconcile: health log %s/%s: %v", key.Ticket, key.Attempt, err)
		return
	}
	line, err := json.Marshal(ev)
	if err != nil {
		r.opt.Logf("reconcile: health log %s/%s: marshal: %v", key.Ticket, key.Attempt, err)
		return
	}
	f, err := os.OpenFile(r.opt.Root.SessionCtlLogPath(key.Ticket, key.Attempt), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		r.opt.Logf("reconcile: health log %s/%s: open: %v", key.Ticket, key.Attempt, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		r.opt.Logf("reconcile: health log %s/%s: write: %v", key.Ticket, key.Attempt, err)
	}
}
