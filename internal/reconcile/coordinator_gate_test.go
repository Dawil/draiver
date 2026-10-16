package reconcile_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// --- coordinator dormancy gate fixtures (drvctl-044) ------------------------

// writeSpecWantsRequires writes a coordinator's spec.md carrying both a `wants:`
// list (the edge that makes it a coordinator) and a `requires:` ordering list —
// the odpp-007 shape: a coordinator whose forward gate is *already open* (its
// `requires:` predecessor is Done) yet must still sit Pending on the dormancy gate.
func writeSpecWantsRequires(t *testing.T, root store.Root, ticket string, wants, requires []string) {
	t.Helper()
	if err := root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("---\nid: %s\nwants: [%s]\nrequires: [%s]\n---\n\nbody\n", ticket, joinCSV(wants), joinCSV(requires))
	if err := os.WriteFile(root.SpecPath(ticket), []byte(body), 0o644); err != nil {
		t.Fatalf("write spec %s: %v", ticket, err)
	}
}

// --- tests ------------------------------------------------------------------

// TestCoordinatorHeldPendingOnEnable is the headline acceptance path (drvctl-044
// #1), the odpp-002 shape: enabling a `wants:`-only coordinator (no ordering gate)
// pulls its children into the fleet but spawns *no* coordinator session — the
// parent sits a dormant Pending shell.
func TestCoordinatorHeldPendingOnEnable(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	capAtt := w.newTicket(t, "CAP") // enabled + Running; a coordinator, so held Pending
	writeSpecWants(t, w.root, "CAP", "C1", "C2", "C3")
	c1 := w.newDisabledTicket(t, "C1")
	c2 := w.newDisabledTicket(t, "C2")
	c3 := w.newDisabledTicket(t, "C3")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// The three children are admitted; the coordinator is not.
	waitFor(t, "all three children admitted", func() bool { return f.count() == 3 })
	for _, c := range []struct{ ticket, att string }{{"C1", c1}, {"C2", c2}, {"C3", c3}} {
		if !broughtUp(t, w.root, c.ticket, c.att) {
			t.Fatalf("child %s/%s was not pulled into the fleet", c.ticket, c.att)
		}
	}
	if broughtUp(t, w.root, "CAP", capAtt) {
		t.Fatal("a wants:-only coordinator was admitted on enable — it must stay a dormant Pending shell")
	}
}

// TestCoordinatorWithOpenRequiresGateHeldPending is acceptance #2, the odpp-007
// shape: a coordinator whose `requires:` predecessor is *already Done* — so the
// forward gate is open and would admit it — is still held Pending by the dormancy
// gate. This is the case the forward gate alone can never catch, since it only
// trims an attempt with an *unmet* predecessor.
func TestCoordinatorWithOpenRequiresGateHeldPending(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	// DEP is Done, so CAP's requires: gate is open from tick 1.
	depAtt := w.newDisabledTicket(t, "DEP")
	reachDone(t, w.root, "DEP", depAtt)

	capAtt := w.newTicket(t, "CAP") // enabled + Running; forward gate open, dormancy gate shut
	writeSpecWantsRequires(t, w.root, "CAP", []string{"CH"}, []string{"DEP"})
	chAtt := w.newDisabledTicket(t, "CH")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// Only the child comes up; the coordinator stays dormant despite its open gate.
	waitFor(t, "child admitted", func() bool { return f.count() == 1 })
	if !broughtUp(t, w.root, "CH", chAtt) {
		t.Fatal("the coordinator's child was not pulled into the fleet")
	}
	if broughtUp(t, w.root, "CAP", capAtt) {
		t.Fatal("a coordinator with an already-open requires: gate was admitted — the dormancy gate must still hold it Pending")
	}
}

// TestCoordinatorAdmittedWhenAllChildrenDone is acceptance #4's positive half: a
// passthrough coordinator (the default) whose every wanted child has reached Done
// is admitted for its own final `review` claim — the design's
// `(all children Done) --> Running` edge.
func TestCoordinatorAdmittedWhenAllChildrenDone(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	capAtt := w.newTicket(t, "CAP") // enabled + Running; passthrough (fleet default)
	writeSpecWants(t, w.root, "CAP", "C1", "C2")
	c1 := w.newDisabledTicket(t, "C1")
	c2 := w.newDisabledTicket(t, "C2")
	reachDone(t, w.root, "C1", c1)
	reachDone(t, w.root, "C2", c2)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// Both children are terminal (never admitted), so the coordinator alone comes up.
	waitFor(t, "coordinator admitted for its final review", func() bool { return broughtUp(t, w.root, "CAP", capAtt) })
	if broughtUp(t, w.root, "C1", c1) || broughtUp(t, w.root, "C2", c2) {
		t.Fatal("a Done child was force-admitted — only the coordinator should come up for its final review")
	}
	if n := f.count(); n != 1 {
		t.Fatalf("expected only the coordinator admitted, got %d spawns", n)
	}
}

// TestCoordinatorPartialChildrenDoneStillHeld guards the boundary of the positive
// condition: with one child Done but another still Running, the epic is not
// complete, so the coordinator stays a Pending shell — it is admitted only once
// *every* wanted child is Done.
func TestCoordinatorPartialChildrenDoneStillHeld(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	capAtt := w.newTicket(t, "CAP")
	writeSpecWants(t, w.root, "CAP", "C1", "C2")
	c1 := w.newDisabledTicket(t, "C1")
	c2 := w.newDisabledTicket(t, "C2")
	reachDone(t, w.root, "C1", c1) // only C1 is Done; C2 is still Running

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// C2 (still Running) is pulled in; C1 (Done) is left alone; CAP stays dormant.
	waitFor(t, "the still-running child admitted", func() bool { return broughtUp(t, w.root, "C2", c2) })
	if broughtUp(t, w.root, "CAP", capAtt) {
		t.Fatal("a coordinator with a still-running child was admitted — it must wait until every child is Done")
	}
	if broughtUp(t, w.root, "C1", c1) {
		t.Fatal("a Done child was force-admitted by wants: propagation")
	}
}

// TestCoordinatorLeafEnableUnchanged is acceptance #5: a leaf ticket (no `wants:`
// edges) is unaffected by the dormancy gate — enabling it still admits it at once.
func TestCoordinatorLeafEnableUnchanged(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	leafAtt := w.newTicket(t, "LEAF") // enabled + Running, no wants: edges

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	waitFor(t, "leaf admitted", func() bool { return broughtUp(t, w.root, "LEAF", leafAtt) })
}

// TestCoordinatorReconvergesToPendingAfterWake is acceptance #3 plus the migration
// invariant (#6), updated for the drvctl-051 edge-triggered activation model: a
// pre-digest coordinator wakes to Running on a child-escalation doorbell, then
// returns to Pending once it `park`s. The withdrawal trigger changed with the
// generalised activation: the old level-triggered edge reaped the coordinator the
// instant the *child escalation resolved*; now a woken manager holds an in-flight
// activation (`woken-by` with no later `park`) and stays desired until **it itself
// parks** (drvctl-052's completion-gate reconciliation). The dormancy gate is still
// deliberately *not* fire-once, so once the park flips the phase to dormant the
// already-admitted coordinator is trimmed and retired back to Pending on the next
// reconcile — the same no-manual-disable/re-enable reconvergence, now driven by the
// manager's own hand-off rather than an external resolution.
func TestCoordinatorReconvergesToPendingAfterWake(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	capAtt := w.newTicket(t, "CAP") // enabled + Running
	writeSpecWants(t, w.root, "CAP", "CH")
	setSupervision(t, w.root, "CAP", capAtt, project.SupervisionPreDigest) // wakes on child escalation
	chAtt := w.newDisabledTicket(t, "CH")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	// Tick 1: CAP is held dormant; only CH comes up.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	waitFor(t, "child admitted, coordinator held", func() bool { return f.count() == 1 })
	if broughtUp(t, w.root, "CAP", capAtt) {
		t.Fatal("the coordinator was admitted before any escalation — it should be dormant")
	}

	// The child escalates → the pre-digest coordinator wakes, and the reconciler
	// authors a `woken-by CH #<esc>` activation on CAP's own log (the dedup marker).
	if _, err := ticketlog.Append(w.root, "CH", chAtt, event.Event{Type: "escalation", Actor: "agent:x", Body: "need a decision"}); err != nil {
		t.Fatalf("escalate CH: %v", err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	waitFor(t, "coordinator woken by child escalation", func() bool { return broughtUp(t, w.root, "CAP", capAtt) })

	// Grab CAP's live adapter once its brief lands, so a reap is observable as a Kill.
	var capAd *fakeAdapter
	waitFor(t, "coordinator brief injected", func() bool {
		capAd = f.briefAdapter("CAP")
		return capAd != nil
	})

	// The manager handles the doorbell and hands off with a `park` (the serial-replay
	// stop signal): a park later than its activation flips the phase to dormant,
	// removing the in-flight marker that kept it desired.
	if _, err := ticketlog.Append(w.root, "CAP", capAtt, event.Event{Type: "park", Actor: "agent:claude-code", Body: "handled CH; nothing else needs me"}); err != nil {
		t.Fatalf("park CAP: %v", err)
	}

	// Tick 3: CAP is dormant (parked past its activation) but still live, so the
	// dormancy gate trims it out of the desired set and it is retired — returning to
	// Pending without a manual disable/re-enable.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	waitFor(t, "parked coordinator reaped back to Pending", func() bool { return capAd.wasKilled() })

	// Tick 4: the CH escalation doorbell is handled (its activation is on CAP's log),
	// so CAP is not re-woken — it stays Pending. A spurious re-wake would spawn a new
	// session; assert the spawn count held at the two (CH + the one CAP wake).
	before := f.count()
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 4: %v", err)
	}
	if n := f.count(); n != before {
		t.Fatalf("a handled doorbell re-woke the coordinator: %d spawns, want %d — edge-trigger dedup failed", n, before)
	}
}
