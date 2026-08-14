// This file holds the forward success gate — the reconcile loop's first real
// scheduler increment (drvctl-040). `after:`/`requires:` edges (drvctl-037) are
// an *ordering* gate: a dependent is held out of admission (Pending) until its
// ordering predecessors succeed. admit/desired (in reconcile.go) own the desired
// set; this file is the one filter that trims it, concentrated so the gate's two
// subtleties — which threshold each relation waits for, and the fire-once latch —
// can be reviewed on their own.
//
//   - `after: [X]`    admits once X reaches **Review** (a success *claim*).
//   - `requires: [X]` admits once X reaches **Done** (the *merged* terminal).
//
// The gate is **edge-triggered and fire-once**: it holds only a *never-admitted*
// dependent out. Once a dependent has been admitted (a session id is on record),
// a later change in a predecessor — a Review flickering back to Running — never
// re-closes the gate on it. Staleness surfaces at the integration test, not here
// (docs/capabilities-and-supervision.md §Attempt admission).
package reconcile

import (
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/worktree"
)

// reachedState captures how far a predecessor ticket's work has progressed — the
// two thresholds the forward gate reads. Both are the *best* (most-succeeded)
// state seen across any of the ticket's attempts: a ticket has "reached Review"
// when a review claim exists on any attempt, "reached Done" when any attempt is
// closed. Success is monotonic across a ticket's attempts, so a later re-run
// (a fresh attempt) never retracts a threshold an earlier attempt already
// crossed — the gate reads the predecessor's earned success, not its latest
// attempt's momentary state (decision #3).
type reachedState struct {
	review bool // an attempt is at Review or Done
	done   bool // an attempt is at Done
}

// bestReached folds every attempt's derived control state into a per-ticket
// reachedState. `all` is project.LoadAll's output (every attempt of every ticket
// under the root), so one pass covers the fleet. A ticket absent from the map
// (no attempts, or none past Running/Needs-me) has the zero value — no threshold
// reached — which correctly holds any dependent's gate shut.
func bestReached(all []project.Attempt) map[string]reachedState {
	out := map[string]reachedState{}
	for _, a := range all {
		rs := out[a.Ticket]
		switch a.State {
		case project.Done:
			rs.done = true
			rs.review = true // Done is past Review, so it satisfies `after:` too
		case project.Review:
			rs.review = true
		}
		out[a.Ticket] = rs
	}
	return out
}

// applyForwardGate holds Pending any desired attempt whose ordering predecessors
// have not yet succeeded (drvctl-040). It runs last in desired(), after the
// direct-enable, imperative-marker, and wants:-propagation passes have built the
// desired set `out`: enable still flows down `wants:` from a gate-held (Pending)
// parent, so the gate trims *admission* without disturbing the desired-ness a
// Pending coordinator sources to its children — each child then subject to its
// own gate here.
//
// A dropped key is simply absent from the desired set, so the tick never admits
// it and (having never run) it is not in the current-run table for retire to
// reap: it sits Pending, re-checked every tick, and admitted the tick after its
// gate opens.
func (r *Reconciler) applyForwardGate(all []project.Attempt, out map[worktree.Key]project.Attempt) error {
	if len(out) == 0 {
		return nil // nothing desired ⇒ nothing to gate; skip the edge read.
	}
	edges, err := project.LoadAllEdges(r.opt.Root)
	if err != nil {
		return err
	}
	reached := bestReached(all)

	for key := range out {
		e := edges[key.Ticket]
		if len(e.After) == 0 && len(e.Requires) == 0 {
			continue // ungated — no ordering predecessors.
		}
		if gateOpen(e, reached) {
			continue // predecessors have succeeded; admit this tick.
		}
		// Gate still shut. Fire-once latch: an attempt already admitted — a session
		// id is on record from a prior tick or a previous daemon boot — is never
		// re-gated, so a predecessor flickering Review↔Running cannot retract an
		// already-running dependent. Only a never-admitted dependent is held Pending.
		if r.admitted(key) {
			continue
		}
		delete(out, key)
	}
	return nil
}

// gateOpen reports whether every ordering predecessor named in a dependent's
// edges has crossed its required threshold: each `after:` predecessor at
// Review-or-Done, each `requires:` predecessor at Done. A single unmet
// predecessor holds the gate shut (all must succeed), and a predecessor with no
// reached threshold (unknown ticket, or one still Running/Needs-me) is unmet.
func gateOpen(e project.Edges, reached map[string]reachedState) bool {
	for _, x := range e.After {
		if !reached[x].review {
			return false
		}
	}
	for _, x := range e.Requires {
		if !reached[x].done {
			return false
		}
	}
	return true
}

// admitted reports whether an attempt has ever been brought up — a session id is
// on record. It is the durable, restart-surviving fire-once latch: session.json
// is written the tick an attempt is first admitted and persists across a park or
// a ctld restart (the daemon holds no authoritative in-memory state), so the gate
// never re-closes on an attempt that already ran.
func (r *Reconciler) admitted(key worktree.Key) bool {
	id, ok := r.readIdentity(key)
	return ok && id.SessionID != ""
}
