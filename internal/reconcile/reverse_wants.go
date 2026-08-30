// This file holds reverse-`wants:` activation — the reconcile loop's second
// scheduler increment (drvctl-041), systemd's **`OnFailure=`** analog. The
// forward gate (forward_gate.go) wakes a *successor* when its predecessor
// succeeds; this is the mirror edge — a sub-ticket's **escalation** wakes the
// dormant (Pending) **Capability** that `wants:` it, so a coordinator can assess.
//
// Enable flows *down* the `wants:` edge (drvctl-038); escalation flows *up* the
// same edge. Children stay fully decoupled — they never name the coordinator
// (docs/capabilities-and-supervision.md §The two edge directions):
//
//	Activation rule: on an escalation in ticket X's log, activate every Pending
//	ticket W whose spec.md `wants:` X.
//
// It is an **additive** contributor to `desired()` (parallel to propagateWants),
// so it can only *add* a wake and never regress the direct/wants/gate passes.
package reconcile

import (
	"sort"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/worktree"
)

// activateReverseWants extends the desired set *up* the `wants:` edge: on an open
// escalation in a wanted child X's log, every ticket W that `wants:` X has its
// dormant coordinator woken (drvctl-041). It runs **last in desired(), after the
// forward gate**, so a wanted child's escalation wakes a coordinator even while
// its own `after:`/`requires:` gate is still shut — the wake overriding the
// forward gate is the whole point of the reverse (`OnFailure=`) edge.
//
// It is deliberately narrower than propagateWants's desireWantedChild:
//
//   - It does not mint — a coordinator with no attempt is not a dormant/Pending
//     coordinator there is anything to wake.
//   - It requires the wanter be Enabled — a human `disable` is a "leave it alone"
//     signal a child's escalation must not force-override. (An already-desired W
//     is in `out` from an earlier pass, so the union is a harmless no-op.)
//
// Withdrawal needs no code: desired() is recomputed every tick, so once the child
// escalation is resolved (the child leaves NeedsMe) the wake simply stops being
// re-sourced next tick — the same symmetric withdrawal propagateWants gets free.
func (r *Reconciler) activateReverseWants(all []project.Attempt, out map[worktree.Key]project.Attempt) error {
	// Index attempts by ticket (last entry is the latest — LoadAll is sorted), and
	// note which tickets carry an open escalation. A ticket is "escalating" when any
	// of its attempts derives NeedsMe (an escalation with no later resolution); a
	// resolved escalation has already returned the child to its own work and should
	// not keep re-waking the parent.
	byTicket := map[string][]project.Attempt{}
	escalating := map[string]bool{}
	for _, a := range all {
		byTicket[a.Ticket] = append(byTicket[a.Ticket], a)
		if a.State == project.NeedsMe {
			escalating[a.Ticket] = true
		}
	}
	if len(escalating) == 0 {
		return nil // no open escalation ⇒ nothing to activate; skip the edge read.
	}

	reverse, err := project.ReverseWants(r.opt.Root)
	if err != nil {
		return err
	}

	// Walk escalating children in a stable order so any operational log line is
	// deterministic across ticks; reverse[x] is already sorted (project.ReverseWants).
	children := make([]string, 0, len(escalating))
	for x := range escalating {
		children = append(children, x)
	}
	sort.Strings(children)
	for _, x := range children {
		for _, w := range reverse[x] {
			r.wakeWanter(w, byTicket[w], out)
		}
	}
	return nil
}

// wakeWanter adds a coordinator W's latest attempt to the desired set when it is a
// genuine dormant coordinator to wake — an Enabled attempt whose latest is
// Running. A W with no attempt (nothing to wake), a disabled W (respect the
// human's park), or a W whose latest is a Review claim / Needs-me block / closed
// Done (not dormant, and never force-admitted) is left untouched.
//
// The wake is further gated by W's supervision dial (drvctl-042): only a coordinator
// whose effective mode is pre-digest is woken to assess. Under passthrough (the
// floor) the child's escalation routes straight to Needs-me and W stays a dormant
// Pending shell — no session spawned — until its own final review. The effective
// mode folds W's per-ticket `supervision` override over the fleet default, so the
// mode is read from config and overridable per ticket.
func (r *Reconciler) wakeWanter(ticket string, attempts []project.Attempt, out map[worktree.Key]project.Attempt) {
	if len(attempts) == 0 {
		return
	}
	latest := attempts[len(attempts)-1]
	if !latest.Enabled || latest.State != project.Running {
		return
	}
	if project.Effective(latest.Supervision, r.opt.DefaultSupervision) != project.SupervisionPreDigest {
		return
	}
	out[worktree.Key{Ticket: latest.Ticket, Attempt: latest.ID}] = latest
}
