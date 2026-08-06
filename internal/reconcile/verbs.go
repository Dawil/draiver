// This file holds draiverctld's imperative client verbs — the "systemctl" surface
// the `draiver ctl` client drives, orthogonal to reconcile.go's declarative loop.
//
// Start/Stop/Restart/EnableNow are the imperative overrides the client exposes —
// the "systemctl" verbs to the reconcile loop's "PID 1". Start and Restart are
// control-plane actions: they do not bring a session up themselves (an
// unsupervised orphan that dies with the invoking shell), they hand the attempt
// off to a running `ctl up` by writing the transient desired-marker the loop
// reconciles — so the self-heal cascade and brief-on-reset coupling live in the
// one daemon-shared bringUp and the client path cannot drift from it (drvctl-016).
// Stop is a direct reap (it only needs the recorded pid, and reaping a process
// wants no supervisor) that also clears the marker, so a stopped attempt leaves
// the imperative fleet and stays stopped.
//
// Everything here — the verbs, their StartResult/StopResult/RestartResult/
// EnableResult/FlushLevel result types, and the handoff/reap/fork/flush/park
// helpers — is the "PID-1 handoff" surface (drvctl-016), concentrated so it can
// be reviewed apart from the loop. The retire recorder (recordRetire) stays with
// the loop in reconcile.go; only the fork/flush recorders live here.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// ErrNoController is returned by the imperative handoff verbs when no `ctl up` is
// running to hand the attempt off to — the "requires PID 1" gate that keeps a
// backgrounded start/restart from spawning an unsupervised orphan (drvctl-016).
var ErrNoController = errors.New("no running `ctl up` (start the supervisor with `ctl up` first)")

// requireController returns the live controller or ErrNoController — the
// require-pid-1 gate shared by the handoff verbs (start/restart). It refuses
// rather than let a verb act with no supervisor to own the resulting session.
func (r *Reconciler) requireController() (Controller, error) {
	ctrl, ok := r.liveController()
	if !ok {
		return Controller{}, ErrNoController
	}
	return ctrl, nil
}

// handOff hands an attempt to the live controller so the daemon's next tick
// brings it up (or back up) through the shared bringUp cascade. For a not-yet-
// desired (disabled) attempt it writes the transient desired-marker stamped with
// the controller's boot nonce — the stamp is what makes the handoff transient (a
// restarted daemon has a new nonce and sweeps it). An already-enabled attempt is
// left unmarked: the durable enable bit already makes it desired, and because
// desired() short-circuits Enabled before reading the marker, a redundant marker
// would outlive a later `disable` and leave the attempt stuck desired. Such a
// target is re-admitted on the enable bit alone (reclaimSpent), so no marker is
// needed — decision #29 (drvctl-016).
func (r *Reconciler) handOff(ctrl Controller, ticket, attempt string) error {
	a, err := project.LoadAttempt(r.opt.Root, ticket, attempt)
	if err != nil {
		return err
	}
	if a.Enabled {
		return nil
	}
	return writeDesiredMarker(r.opt.Root, ticket, attempt, desiredMarker{Nonce: ctrl.Nonce, Stamp: r.opt.Now()})
}

// StartResult reports how an imperative Start was handed off, so the client can
// print a truthful message: which supervisor now owns the attempt, and — for
// start --new-attempt — the forked id it created.
type StartResult struct {
	// ControllerPID is the pid of the running `ctl up` the attempt was handed to.
	ControllerPID int
	// Attempt is the attempt actually handed off: the target, or the fork's new id
	// when Forked.
	Attempt string
	// Forked and From are set by start --new-attempt — a new attempt was created
	// from From and handed off, leaving the parent as it was (drvctl-016).
	Forked bool
	From   string
}

// Start hands one attempt off to a running `ctl up` to bring up in the
// background, rather than driving a session itself. It requires a live
// controller (ErrNoController otherwise) and writes a transient desired-marker
// stamped with that controller's nonce; the daemon's next tick unions the
// marker into its desired set and admits the attempt through the same cascade
// (resume the recorded session, else spawn fresh) and brief-on-reset coupling
// its enabled fleet uses. The marker is transient: it is swept if the daemon
// restarts (new nonce), so an imperative start dies with its supervisor —
// distinct from `enable`, the durable, restart-surviving opt-in. Streaming is
// no longer part of Start; watch the handed-off session with `ctl logs -f`.
func (r *Reconciler) Start(ticket, attempt string) (StartResult, error) {
	if _, err := project.LoadAttempt(r.opt.Root, ticket, attempt); err != nil {
		return StartResult{}, err
	}
	ctrl, err := r.requireController()
	if err != nil {
		return StartResult{}, err
	}
	if err := r.handOff(ctrl, ticket, attempt); err != nil {
		return StartResult{}, err
	}
	return StartResult{ControllerPID: ctrl.PID, Attempt: attempt}, nil
}

// StartNewAttempt forks a new attempt off the target and hands the *fork* to the
// daemon, leaving the parent exactly as it was — a parallel branch, not a reset
// (decision #23). Where `restart --new-attempt` abandons the current path (it
// reaps and parks the parent), `start --new-attempt` opens a second path
// alongside it: the parent keeps running if it was, the child starts fresh. It
// requires pid 1 before forking, so a fork is never created with no supervisor
// to run it.
func (r *Reconciler) StartNewAttempt(ticket, parent string) (StartResult, error) {
	if _, err := project.LoadAttempt(r.opt.Root, ticket, parent); err != nil {
		return StartResult{}, err
	}
	ctrl, err := r.requireController()
	if err != nil {
		return StartResult{}, err
	}
	newID, err := r.forkAttempt(ticket, parent)
	if err != nil {
		return StartResult{}, err
	}
	r.recordStartFork(ticket, parent, newID)
	if err := r.handOff(ctrl, ticket, newID); err != nil {
		return StartResult{}, err
	}
	return StartResult{ControllerPID: ctrl.PID, Attempt: newID, Forked: true, From: parent}, nil
}

// EnableResult reports an `enable --now` handoff: the attempt is now durably
// enabled and the running supervisor will bring it up.
type EnableResult struct {
	// ControllerPID is the pid of the `ctl up` that will bring the attempt up.
	ControllerPID int
	// Seq is the enable event's sequence number in the attempt log.
	Seq int
}

// EnableNow is the imperative half of `enable --now`: it couples the durable
// enable opt-in with an immediate supervised start. Like start/restart it
// requires a live `ctl up` (ErrNoController otherwise), checked UP FRONT so a
// no-daemon invocation persists nothing — the caller can fall back to plain
// `enable` to record the preference for a later daemon, or start one with
// `ctl up`. With a controller present it appends the durable `enable` event;
// the enable bit alone makes the attempt desired, so the daemon's next tick
// brings it up through the same cascade the enabled fleet uses. No transient
// marker is written: enable is the durable opt-in, and a redundant marker on an
// enabled attempt would outlive a later `disable` (handOff skips it for the same
// reason — decision #29). This makes enable --now the PERSISTENT counterpart of
// `start`: start is transient (swept when the daemon restarts), enable --now
// survives and drives auto-restart.
func (r *Reconciler) EnableNow(ticket, attempt string) (EnableResult, error) {
	if _, err := project.LoadAttempt(r.opt.Root, ticket, attempt); err != nil {
		return EnableResult{}, err
	}
	ctrl, err := r.requireController()
	if err != nil {
		return EnableResult{}, err
	}
	// Same body as plain `enable` so the durable log is uniform regardless of
	// which path recorded it — the --now is a CLI convenience, not a durable
	// distinction.
	e, err := ticketlog.Append(r.opt.Root, ticket, attempt, event.Event{
		Type:  "enable",
		Actor: r.opt.Actor,
		Body:  fmt.Sprintf("Supervision enabled for %s/%s.", ticket, attempt),
	})
	if err != nil {
		return EnableResult{}, err
	}
	return EnableResult{ControllerPID: ctrl.PID, Seq: e.Seq}, nil
}

// StopResult reports what Stop did, so the client can print a truthful message.
type StopResult struct {
	// PID is the process Stop found on record (0 if none / already cleared).
	PID int
	// Signaled is true when a live process was told to stop.
	Signaled bool
	// AlreadyStopped is true when there was no live process to signal.
	AlreadyStopped bool
}

// reap terminates an attempt's recorded session process and clears the now-stale
// pid from session.json, keeping the session id, worktree, and log intact — the
// cattle handle survives for a later Resume. It is the teardown shared by the
// public Stop verb and restart's flush, and deliberately does NOT touch the
// desired-marker: Stop removes it to leave the fleet, while restart rewrites it
// to re-request the bring-up. It works on any recorded session regardless of who
// started it (the daemon or a re-adopted foreign process), because all it needs
// is the pid on disk. Reaping an attempt with no session, or one already
// stopped, is not an error: the goal state (not running) already holds.
func (r *Reconciler) reap(ticket, attempt string) (StopResult, error) {
	sess, err := session.Open(r.opt.Root, ticket, attempt)
	if err != nil {
		return StopResult{}, err
	}
	defer sess.Close()

	id, err := sess.ReadIdentity()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return StopResult{AlreadyStopped: true}, nil // never had a session
		}
		return StopResult{}, err
	}

	res := StopResult{PID: id.PID}
	if id.PID != 0 && r.opt.Proc.Alive(id.PID) {
		if err := r.opt.Proc.Terminate(id.PID); err != nil {
			return res, fmt.Errorf("reconcile: stop %s/%s (pid %d): %w", ticket, attempt, id.PID, err)
		}
		res.Signaled = true
	} else {
		res.AlreadyStopped = true
	}

	if id.PID != 0 {
		id.PID = 0
		if err := sess.WriteIdentity(id); err != nil {
			return res, fmt.Errorf("reconcile: clear pid %s/%s: %w", ticket, attempt, err)
		}
	}
	return res, nil
}

// Stop reaps an attempt's recorded session (see reap) and then clears any
// desired-marker it carried, so the attempt leaves the imperative fleet and a
// stopped attempt stays stopped — the symmetric counterpart of `start` writing
// the marker. Without this, a still-desired attempt (enabled, or imperatively
// started) would be re-admitted by the daemon on the next tick. Stopping an
// attempt with no session, or one already stopped, is not an error: the goal
// state (not running) already holds.
func (r *Reconciler) Stop(ticket, attempt string) (StopResult, error) {
	res, err := r.reap(ticket, attempt)
	if err != nil {
		return res, err
	}
	if err := removeDesiredMarker(r.opt.Root, ticket, attempt); err != nil {
		return res, fmt.Errorf("reconcile: clear desired marker %s/%s: %w", ticket, attempt, err)
	}
	return res, nil
}

// FlushLevel selects how deep restart's teardown reaps before the start cascade
// climbs back — a point on the degree axis of drvctl-016's state stack. The
// levels are ordinal and cumulative: a deeper level implies every shallower
// flush. Each step discards one more volatile layer stacked on the durable ticket
// floor (L3):
//
//	FlushNone      reap the process only; the cascade Resumes the same session (L0 kept).
//	FlushSession   + discard the session conversation (L0); the cascade spawns a
//	               fresh session on the surviving worktree, cold-started from the brief.
//	FlushWorktree  + discard the worktree checkout and branch (L1); the cascade
//	               rebuilds the worktree from HEAD before the fresh spawn.
//	FlushAttempt   + discard the attempt (L2) — this FORKS a new attempt (new id,
//	               `from` provenance) and climbs into it; the prior attempt's log is
//	               preserved untouched as an immutable record.
type FlushLevel int

const (
	FlushNone FlushLevel = iota
	FlushSession
	FlushWorktree
	FlushAttempt
)

// RestartResult reports what a Restart flushed, which attempt the daemon will
// bring back up — the same attempt for an in-place restart, or the new fork's id
// when the depth reached FlushAttempt — and which controller it was handed to.
// The client renders it to report the plan (in particular a fork's new id and
// the parked parent) up front.
type RestartResult struct {
	Level         FlushLevel
	Ticket        string
	Attempt       string // the attempt handed off (the fork's id for FlushAttempt)
	Forked        bool   // true when FlushAttempt created a new attempt
	From          string // the parent attempt id, set when Forked
	ControllerPID int    // the `ctl up` the restart handed the attempt off to
}

// Restart reaps the current session, flushes volatile state to the chosen depth,
// then hands the attempt off to a running `ctl up` to bring back up in the
// background — the same imperative-transient handoff `start` uses (drvctl-016
// Phase C2). It requires pid 1 (ErrNoController otherwise) and returns without
// blocking on a session; watch the brought-up attempt with `ctl logs -f`.
//
// level selects how deep the teardown flushes before the daemon's start cascade
// climbs back — the degree axis (see FlushLevel). FlushNone is the clean
// "continue" the old hybrid restart failed to be: the cascade Resumes the same
// session and does not re-brief. FlushAttempt is the one branch point where
// restart stops mutating in place and forks a new attempt, parking the parent
// (disable + marker sweep) so the daemon runs the fork and not both (decision
// #23); the fork is what RestartResult.Forked/From report.
//
// The reap keeps the desired-marker (unlike the public Stop, which clears it);
// the handoff below rewrites it. For an in-place restart that rewritten marker
// is the re-admit signal the daemon honours over the spent, intentionally-
// stopped run (see readmittable).
func (r *Reconciler) Restart(ctx context.Context, ticket, attempt string, level FlushLevel) (RestartResult, error) {
	ctrl, err := r.requireController()
	if err != nil {
		return RestartResult{}, err
	}

	// The shallowest teardown always happens: reap the running process, keeping the
	// durable layers (and the marker) for the flush/cascade to act on.
	if _, err := r.reap(ticket, attempt); err != nil {
		return RestartResult{}, err
	}

	res := RestartResult{Level: level, Ticket: ticket, Attempt: attempt, ControllerPID: ctrl.PID}

	switch {
	case level >= FlushAttempt:
		// Stop mutating in place and branch: fork a new attempt and climb into it.
		newID, err := r.forkAttempt(ticket, attempt)
		if err != nil {
			return RestartResult{}, err
		}
		r.recordFork(ticket, attempt, newID)
		// Park the parent so the daemon does not run it alongside the child
		// (decision #23): restart --new-attempt abandons this path for a fresh one.
		if err := r.parkAttempt(ticket, attempt); err != nil {
			return RestartResult{}, err
		}
		res.Attempt, res.Forked, res.From = newID, true, attempt
	case level >= FlushWorktree:
		a, err := project.LoadAttempt(r.opt.Root, ticket, attempt)
		if err != nil {
			return RestartResult{}, err
		}
		if err := r.flushSession(ticket, attempt); err != nil {
			return RestartResult{}, err
		}
		if err := r.flushWorktree(ctx, a); err != nil {
			return RestartResult{}, err
		}
		r.recordFlush(ticket, attempt, FlushWorktree)
	case level >= FlushSession:
		if err := r.flushSession(ticket, attempt); err != nil {
			return RestartResult{}, err
		}
		r.recordFlush(ticket, attempt, FlushSession)
	}

	// Hand the (possibly new) target off to the daemon.
	if err := r.handOff(ctrl, res.Ticket, res.Attempt); err != nil {
		return RestartResult{}, err
	}
	return res, nil
}

// parkAttempt takes an attempt out of both desired sets: it appends a `disable`
// event (leaving the declarative fleet) and sweeps any imperative desired-marker.
// It is how `restart --new-attempt` retires the parent after forking a child off
// it, so the daemon runs the fork and not both (decision #23). The parent's log
// is untouched (recordFork already noted the branch on it); the daemon's normal
// disable→retire reaps its session and reclaims a clean worktree, and the session
// id survives on disk (Kill keeps the cattle handle) so the parked attempt stays
// resumable.
func (r *Reconciler) parkAttempt(ticket, attempt string) error {
	if _, err := ticketlog.Append(r.opt.Root, ticket, attempt, event.Event{
		Type:  "disable",
		Actor: r.opt.Actor,
		Body:  fmt.Sprintf("Parked %s/%s on fork: restart --new-attempt forked a new attempt from it, so it leaves the supervised fleet. Its log is preserved as an immutable record of the path taken so far.", ticket, attempt),
	}); err != nil {
		return fmt.Errorf("reconcile: park %s/%s on fork: %w", ticket, attempt, err)
	}
	if err := removeDesiredMarker(r.opt.Root, ticket, attempt); err != nil {
		return fmt.Errorf("reconcile: sweep marker for parked %s/%s: %w", ticket, attempt, err)
	}
	return nil
}

// flushSession discards an attempt's session conversation (L0): it clears the
// recorded session id so the start cascade cannot Resume the old conversation and
// instead Spawns a fresh session on the surviving worktree (which then cold-starts
// from the brief). The pid was already cleared by the preceding Stop. An attempt
// with no session on record — or one whose id is already empty — is a no-op: there
// is no conversation to discard. Only the id is cleared; the meter and stream tee
// carry across exactly as they do when the Phase-A cascade falls through a dead id
// to a fresh spawn.
func (r *Reconciler) flushSession(ticket, attempt string) error {
	sess, err := session.Open(r.opt.Root, ticket, attempt)
	if err != nil {
		return err
	}
	defer sess.Close()

	id, err := sess.ReadIdentity()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // no session on record — nothing to flush
		}
		return err
	}
	if id.SessionID == "" {
		return nil
	}
	id.SessionID = ""
	id.PID = 0
	return sess.WriteIdentity(id)
}

// flushWorktree discards an attempt's worktree (L1): it force-removes the checkout
// and deletes the per-attempt branch, so the start cascade rebuilds a fresh
// worktree from HEAD rather than re-attaching to the surviving branch. The force
// is deliberate — --new-worktree is an explicit request to discard the checkout,
// uncommitted work and all. A worktree that was never created is tolerated as a
// no-op by Remove.
func (r *Reconciler) flushWorktree(ctx context.Context, a project.Attempt) error {
	repo, err := r.repoFor(a)
	if err != nil {
		return err
	}
	wm, err := r.managerFor(repo)
	if err != nil {
		return fmt.Errorf("attempt %s/%s repo %q is missing or not a git working tree: %w", a.Ticket, a.ID, repo, err)
	}
	key := worktree.Key{Ticket: a.Ticket, Attempt: a.ID}
	if err := wm.Remove(ctx, key, worktree.RemoveOptions{Force: true, DeleteBranch: true}); err != nil {
		return fmt.Errorf("flush worktree %s/%s: %w", a.Ticket, a.ID, err)
	}
	return nil
}

// forkAttempt branches a new attempt off parent (the L2 flush): it inherits the
// parent's tool/model/repo and records `from` provenance, leaving the parent's log
// untouched as the immutable record of the path taken so far. It returns the new
// attempt's id, which the start cascade then cold-starts fresh (no session, no
// worktree — Spawn builds both). Repo is inherited so the fork targets the same
// working tree, mirroring `attempt new --from`.
func (r *Reconciler) forkAttempt(ticket, parent string) (string, error) {
	pm, err := attempt.LoadMeta(r.opt.Root, ticket, parent)
	if err != nil {
		return "", err
	}
	m, err := attempt.Create(r.opt.Root, ticket, attempt.New{
		Tool:  pm.Tool,
		Model: pm.Model,
		Repo:  pm.Repo,
		Base:  pm.Base,
		Actor: r.opt.Actor,
		From:  parent,
	})
	if err != nil {
		return "", err
	}
	return m.ID, nil
}

// recordFork appends a note to the parent attempt's log recording that a restart
// forked a new attempt from it, so the branch point — the one place restart lands
// on a *different* attempt — is visible on the board and to a resumed agent.
// Best-effort: a note that cannot be written is logged operationally, never
// failing the restart.
func (r *Reconciler) recordFork(ticket, parent, child string) {
	body := fmt.Sprintf("Forked a new attempt %s/%s from this one (restart --new-attempt); this attempt's log is preserved as an immutable record of the path taken so far.", ticket, child)
	if _, err := ticketlog.Append(r.opt.Root, ticket, parent, event.Event{
		Type:  "note",
		Actor: r.opt.Actor,
		Body:  body,
	}); err != nil {
		r.opt.Logf("reconcile: record fork %s/%s: %v", ticket, parent, err)
	}
}

// recordStartFork notes on the parent that `start --new-attempt` branched a new
// attempt off it while leaving it running — the parallel-branch counterpart of
// recordFork (which restart uses when it instead parks the parent). Best-effort,
// like recordFork.
func (r *Reconciler) recordStartFork(ticket, parent, child string) {
	body := fmt.Sprintf("Branched a new attempt %s/%s from this one (start --new-attempt); this attempt is left running alongside the fork.", ticket, child)
	if _, err := ticketlog.Append(r.opt.Root, ticket, parent, event.Event{
		Type:  "note",
		Actor: r.opt.Actor,
		Body:  body,
	}); err != nil {
		r.opt.Logf("reconcile: record start-fork %s/%s: %v", ticket, parent, err)
	}
}

// recordFlush appends a note recording an in-place restart's teardown depth, so a
// destructive reset (especially --new-worktree, which discards uncommitted work) is
// visible on the board rather than silent. FlushNone and FlushAttempt are recorded
// elsewhere (a bare restart is an unremarkable continue; a fork is recorded on the
// parent). Best-effort, like recordFork.
func (r *Reconciler) recordFlush(ticket, attempt string, level FlushLevel) {
	var what string
	switch level {
	case FlushSession:
		what = "discarded the session conversation (restart --new-session); a fresh session will cold-start from the brief on the same worktree"
	case FlushWorktree:
		what = "discarded the session conversation and the worktree checkout+branch (restart --new-worktree); the worktree will be rebuilt from HEAD and the session cold-started from the brief"
	default:
		return
	}
	if _, err := ticketlog.Append(r.opt.Root, ticket, attempt, event.Event{
		Type:  "note",
		Actor: r.opt.Actor,
		Body:  "Restart flush: " + what + ".",
	}); err != nil {
		r.opt.Logf("reconcile: record flush %s/%s: %v", ticket, attempt, err)
	}
}
