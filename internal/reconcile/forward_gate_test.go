package reconcile_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// --- forward success gate fixtures (drvctl-040) -----------------------------

// writeSpecEdges writes a ticket's spec.md with a single ordering relation
// (`after:` or `requires:`) naming the given predecessor tickets — the edge the
// forward gate reads. Mirrors writeSpecWants; the ticket dir is ensured here so a
// predecessor-only ticket needs no separate setup.
func writeSpecEdges(t *testing.T, root store.Root, ticket, rel string, deps ...string) {
	t.Helper()
	if err := root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("---\nid: %s\n%s: [%s]\n---\n\nbody\n", ticket, rel, joinCSV(deps))
	if err := os.WriteFile(root.SpecPath(ticket), []byte(body), 0o644); err != nil {
		t.Fatalf("write spec %s: %v", ticket, err)
	}
}

// reachReview advances a predecessor attempt to Review by appending a review
// claim to its log.
func reachReview(t *testing.T, root store.Root, ticket, att string) {
	t.Helper()
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "review", Actor: "agent:x", Body: "claim"}); err != nil {
		t.Fatalf("review %s/%s: %v", ticket, att, err)
	}
}

// reachDone closes a predecessor attempt (derives to the terminal Done state).
func reachDone(t *testing.T, root store.Root, ticket, att string) {
	t.Helper()
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "done", Actor: "human:dave", Body: "closed"}); err != nil {
		t.Fatalf("done %s/%s: %v", ticket, att, err)
	}
}

// reopenToRunning reopens a reviewed attempt back to Running by logging a
// decision (the first-class Review → Running transition), the flicker the
// fire-once latch must absorb.
func reopenToRunning(t *testing.T, root store.Root, ticket, att string) {
	t.Helper()
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "decision", Actor: "human:dave", Body: "reopened"}); err != nil {
		t.Fatalf("reopen %s/%s: %v", ticket, att, err)
	}
}

// --- tests ------------------------------------------------------------------

// TestAfterGateHoldsUntilReview is the headline acceptance path for `after:`:
// INFRA-1 (enabled, Running) is held Pending while its predecessor SRC-1 is only
// Running, and admits the tick after SRC-1 reaches Review.
func TestAfterGateHoldsUntilReview(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	// SRC-1: a predecessor ticket, Running (a bare disabled attempt suffices — the
	// gate reads its control state, not its enablement).
	srcAtt := w.newDisabledTicket(t, "SRC-1")
	// INFRA-1: enabled + Running, so it *would* be admitted but for the gate.
	infraAtt := w.newTicket(t, "INFRA-1")
	writeSpecEdges(t, w.root, "INFRA-1", "after", "SRC-1")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	// Tick 1: SRC-1 is only Running, so the `after:` gate holds INFRA-1 Pending.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if broughtUp(t, w.root, "INFRA-1", infraAtt) {
		t.Fatal("INFRA-1 was admitted before SRC-1 reached Review")
	}
	if n := f.count(); n != 0 {
		t.Fatalf("gate should have admitted nothing, got %d spawns", n)
	}

	// SRC-1 reaches Review → the gate opens.
	reachReview(t, w.root, "SRC-1", srcAtt)

	// Tick 2: INFRA-1 admits the tick after its gate is satisfied.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	waitFor(t, "INFRA-1 admitted after SRC-1 reached Review", func() bool {
		return broughtUp(t, w.root, "INFRA-1", infraAtt)
	})
}

// TestRequiresGateHoldsUntilDone is the acceptance path for `requires:`: CAP-B is
// held Pending while CAP-A is Running — and, crucially, while CAP-A is merely at
// Review — and admits only once CAP-A reaches Done. The mid-Review assertion is
// what distinguishes `requires:` (Done) from `after:` (Review).
func TestRequiresGateHoldsUntilDone(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	capA := w.newDisabledTicket(t, "CAP-A")
	capBAtt := w.newTicket(t, "CAP-B")
	writeSpecEdges(t, w.root, "CAP-B", "requires", "CAP-A")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	// CAP-A Running → held.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if broughtUp(t, w.root, "CAP-B", capBAtt) {
		t.Fatal("CAP-B admitted while CAP-A was only Running")
	}

	// CAP-A at Review is NOT enough for `requires:` — it waits for the merged
	// terminal, not the success claim.
	reachReview(t, w.root, "CAP-A", capA)
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick after CAP-A review: %v", err)
	}
	if broughtUp(t, w.root, "CAP-B", capBAtt) {
		t.Fatal("CAP-B admitted on CAP-A Review, but requires: waits for Done")
	}

	// CAP-A reaches Done → the gate opens.
	reachDone(t, w.root, "CAP-A", capA)
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick after CAP-A done: %v", err)
	}
	waitFor(t, "CAP-B admitted after CAP-A reached Done", func() bool {
		return broughtUp(t, w.root, "CAP-B", capBAtt)
	})
}

// TestGateLatchesOnceAdmitted is the fire-once latch: once INFRA-1 is admitted on
// SRC-1's Review, reopening SRC-1 back to Running does not reap or restart
// INFRA-1 — a predecessor flickering Review↔Running must never flap an
// already-admitted dependent.
func TestGateLatchesOnceAdmitted(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	srcAtt := w.newDisabledTicket(t, "SRC-1")
	infraAtt := w.newTicket(t, "INFRA-1")
	writeSpecEdges(t, w.root, "INFRA-1", "after", "SRC-1")

	// SRC-1 already at Review, so the first tick admits INFRA-1.
	reachReview(t, w.root, "SRC-1", srcAtt)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	waitFor(t, "INFRA-1 admitted", func() bool { return broughtUp(t, w.root, "INFRA-1", infraAtt) })

	// Grab INFRA-1's live adapter once its brief has landed, so a reap would be
	// observable as a Kill on it.
	var infra *fakeAdapter
	waitFor(t, "INFRA-1 brief injected", func() bool {
		infra = f.briefAdapter("INFRA-1")
		return infra != nil
	})

	// Reopen SRC-1 back to Running — the predecessor flicker the latch must absorb.
	reopenToRunning(t, w.root, "SRC-1", srcAtt)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("post-reopen tick: %v", err)
	}

	if infra.wasKilled() {
		t.Fatal("INFRA-1 was reaped when SRC-1 reopened to Running — the fire-once latch did not hold")
	}
	if !broughtUp(t, w.root, "INFRA-1", infraAtt) {
		t.Fatal("INFRA-1 lost its session after SRC-1 reopened")
	}
}

// TestAfterGateAllPredecessorsMustSucceed: with `after: [SRC-1, SRC-2]`, the gate
// stays shut until *every* predecessor reaches Review — one alone is not enough.
func TestAfterGateAllPredecessorsMustSucceed(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	src1 := w.newDisabledTicket(t, "SRC-1")
	src2 := w.newDisabledTicket(t, "SRC-2")
	e2eAtt := w.newTicket(t, "E2E-1")
	writeSpecEdges(t, w.root, "E2E-1", "after", "SRC-1", "SRC-2")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	// Only SRC-1 reaches Review — SRC-2 still Running holds the gate shut.
	reachReview(t, w.root, "SRC-1", src1)
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick with one predecessor: %v", err)
	}
	if broughtUp(t, w.root, "E2E-1", e2eAtt) {
		t.Fatal("E2E-1 admitted with only one of two predecessors at Review")
	}

	// SRC-2 reaches Review too → the gate opens.
	reachReview(t, w.root, "SRC-2", src2)
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick with both predecessors: %v", err)
	}
	waitFor(t, "E2E-1 admitted once both predecessors reached Review", func() bool {
		return broughtUp(t, w.root, "E2E-1", e2eAtt)
	})
}
