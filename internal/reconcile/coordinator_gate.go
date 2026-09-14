// This file holds the coordinator dormancy gate — the reconcile loop's third
// scheduler increment (drvctl-044), the piece that makes the documented
// Capability-coordinator lifecycle reachable via `enable`. A `wants:`-coordinator
// is meant to rest **Pending** — a dormant shell that goes Running only when a
// child escalates (pre-digest) or when every child is Done (its final review
// claim). Without this gate the direct-enable pass admits it unconditionally, so
// enabling a coordinator starts it working the whole epic immediately
// (docs/capabilities-and-supervision.md §The coordinator, §The supervision dial).
//
// Like the forward gate (forward_gate.go) it **trims admission, not desired-ness**:
// it runs after propagateWants (so the coordinator's children are already pulled
// into the fleet from the still-desired parent) and removes the parent's own key
// from `out` before admission. It differs from the forward gate in two ways that
// are the whole point:
//
//   - It detects a coordinator by its *own* `wants:` edges, over every key in
//     `out` — not only the directly-`Enabled` ones — so an enabled-via-parent
//     child that is itself a coordinator stays a Pending shell too (enable flows
//     down `wants:`, so dormancy does as well).
//   - It is **not** fire-once. The forward gate never re-gates an already-admitted
//     dependent; this gate must, so a legacy coordinator already carrying a
//     session — admitted by the old buggy admit-on-enable path — is trimmed on the
//     next reconcile and retired back to Pending (the migration acceptance), and a
//     pre-digest coordinator is re-parked once its child escalation resolves.
//
// Reverse-`wants:` activation (reverse_wants.go) still runs *last*, after this
// gate, so a child escalation re-adds (wakes) a pre-digest coordinator this gate
// just trimmed; a passthrough coordinator is not re-added and stays dormant until
// all children Done.
package reconcile

import (
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/worktree"
)

// applyCoordinatorGate holds Pending any desired attempt that is a `wants:`
// coordinator, *unless* all of its wanted children are terminal (Done) — the
// positive condition that admits it for its own final review claim, matching the
// design's `(all children Done) --> Running` edge. A held coordinator is simply
// dropped from the desired set: a never-admitted one sits Pending (re-checked each
// tick), an already-admitted one leaves the desired set and is retired back to
// Pending next tick.
//
// A coordinator is any attempt whose ticket carries `wants:` edges. The
// supervision dial's `auto-execute` mode is out of scope (deferred), but it needs
// no explicit exclusion here: it is not a representable Supervision value, so a
// `wants:` coordinator is always in one of the two implemented modes (passthrough
// or pre-digest) today. Runs after the forward gate and before reverse-`wants:`
// activation (see desired()).
func (r *Reconciler) applyCoordinatorGate(all []project.Attempt, out map[worktree.Key]project.Attempt) error {
	if len(out) == 0 {
		return nil // nothing desired ⇒ no coordinator to hold; skip the edge read.
	}
	edges, err := project.LoadAllEdges(r.opt.Root)
	if err != nil {
		return err
	}
	reached := bestReached(all)

	for key := range out {
		e := edges[key.Ticket]
		if len(e.Wants) == 0 {
			continue // a leaf, not a coordinator — admit exactly as before.
		}
		if allWantedDone(e.Wants, reached) {
			continue // every wanted child is Done: admit for the final review claim.
		}
		// A coordinator with unfinished children stays dormant. Trim it from
		// admission; reverse-`wants:` activation (which runs after this) may still
		// re-add it to wake for a child escalation under pre-digest.
		delete(out, key)
	}
	return nil
}

// allWantedDone reports whether every wanted child has reached Done — the
// best-reached (most-succeeded) state across any of the child's attempts, exactly
// the threshold the forward gate's `requires:` relation waits for. A child with no
// reached threshold (unknown ticket, or one still short of Done) makes the whole
// set unmet, so a coordinator is admitted only once its entire epic has merged.
func allWantedDone(wants []string, reached map[string]reachedState) bool {
	for _, child := range wants {
		if !reached[child].done {
			return false
		}
	}
	return true
}
