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

// --- reverse-`wants:` activation fixtures (drvctl-041) ----------------------

// writeSpecWantsAfter writes a coordinator's spec.md carrying both a `wants:`
// list (the reverse edge escalation travels up) and an `after:` list (the forward
// gate that holds it Pending). Combining them in one frontmatter block is what
// lets a test stand up a genuinely *dormant* enabled coordinator: the `after:`
// gate keeps it out of admission until a wanted child's escalation wakes it.
func writeSpecWantsAfter(t *testing.T, root store.Root, ticket string, wants, after []string) {
	t.Helper()
	if err := root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("---\nid: %s\nwants: [%s]\nafter: [%s]\n---\n\nbody\n", ticket, joinCSV(wants), joinCSV(after))
	if err := os.WriteFile(root.SpecPath(ticket), []byte(body), 0o644); err != nil {
		t.Fatalf("write spec %s: %v", ticket, err)
	}
}

// escalate opens an escalation on an attempt (deriving it to Needs-me), the
// upstream trigger reverse-`wants:` activation fires on.
func escalate(t *testing.T, root store.Root, ticket, att string) {
	t.Helper()
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "escalation", Actor: "agent:x", Body: "blocked; need a decision"}); err != nil {
		t.Fatalf("escalate %s/%s: %v", ticket, att, err)
	}
}

// --- tests ------------------------------------------------------------------

// TestReverseWantsWakesPendingCoordinator is the headline acceptance path
// (drvctl-041 acceptance #1): a dormant, Pending coordinator that `wants:` a
// sub-ticket is woken when that sub-ticket escalates — systemd's `OnFailure=`.
//
// The dormancy is real and in-tree: CAP-A also carries `after: [BLOCK]`, and
// BLOCK never reaches Review, so drvctl-040's forward gate holds CAP-A Pending
// (out of admission). Only the reverse-`wants:` wake — which runs after the gate
// and overrides it — can bring CAP-A up on E2E-1's escalation. (When drvctl-042's
// coordinator lifecycle lands, the same contributor becomes the load-bearing wake
// once dormant coordinators leave the direct desired pass.)
func TestReverseWantsWakesPendingCoordinator(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	capAtt := w.newTicket(t, "CAP-A") // enabled + Running, but gated Pending by after:[BLOCK]
	writeSpecWantsAfter(t, w.root, "CAP-A", []string{"E2E-1"}, []string{"BLOCK"})
	w.newTicket(t, "BLOCK")           // enabled + Running; never reaches Review ⇒ gate stays shut
	e2eAtt := w.newDisabledTicket(t, "E2E-1") // the wanted child that will escalate

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	// Tick 1: CAP-A is held Pending behind its shut `after:` gate. BLOCK (directly
	// enabled) and E2E-1 (pulled in as CAP-A's wanted child) come up; CAP-A does not.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	waitFor(t, "BLOCK + E2E-1 admitted, CAP-A held", func() bool { return f.count() == 2 })
	if broughtUp(t, w.root, "CAP-A", capAtt) {
		t.Fatal("CAP-A was admitted before any escalation — its forward gate should hold it Pending")
	}

	// The wanted child escalates.
	escalate(t, w.root, "E2E-1", e2eAtt)

	// Tick 2: E2E-1's escalation flows up the `wants:` edge and wakes CAP-A, even
	// though its own forward gate is still shut.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	waitFor(t, "CAP-A woken by E2E-1 escalation", func() bool { return broughtUp(t, w.root, "CAP-A", capAtt) })
}

// TestReverseWantsNobodyWantsNoActivation is acceptance #2: an escalation on a
// ticket that nobody `wants:` produces no activation. A separate Pending
// coordinator (gated, but with no `wants:` edge to the escalating ticket) is left
// untouched — the reverse edge, not the mere existence of an escalation, is what
// wakes.
func TestReverseWantsNobodyWantsNoActivation(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	// CAP is a dormant coordinator (gated Pending behind after:[BLOCK]) that wants
	// nothing — in particular, not LONE.
	capAtt := w.newTicket(t, "CAP")
	writeSpecEdges(t, w.root, "CAP", "after", "BLOCK")
	w.newTicket(t, "BLOCK") // enabled Running; keeps CAP's gate shut

	// LONE escalates, but no ticket wants it.
	loneAtt := w.newDisabledTicket(t, "LONE")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	// Only BLOCK comes up: CAP is gated Pending, LONE is neither enabled nor wanted.
	waitFor(t, "only BLOCK admitted", func() bool { return f.count() == 1 })

	escalate(t, w.root, "LONE", loneAtt)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	// A wrong wake would raise the spawn count; assert it stayed at BLOCK alone.
	if n := f.count(); n != 1 {
		t.Fatalf("an escalation on an unwanted ticket triggered %d spawns, want 1 (BLOCK only)", n)
	}
	if broughtUp(t, w.root, "CAP", capAtt) {
		t.Fatal("CAP was woken by an escalation on a ticket it does not want — reverse activation must follow the wants: edge")
	}
	if broughtUp(t, w.root, "LONE", loneAtt) {
		t.Fatal("LONE (Needs-me) should not be admitted")
	}
}

// TestReverseWantsRespectsDisable guards the deliberately narrower semantics: a
// child's escalation must not force-override a human `disable`. DIS wants the
// escalating child but is itself disabled (never desired by any other pass), so
// the only way it could come up is a wake — which the Enabled guard blocks.
func TestReverseWantsRespectsDisable(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	disAtt := w.newDisabledTicket(t, "DIS") // disabled coordinator
	writeSpecWants(t, w.root, "DIS", "CHILD")
	childAtt := w.newDisabledTicket(t, "CHILD")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	// Nothing is enabled, so tick 1 admits nothing.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}

	escalate(t, w.root, "CHILD", childAtt)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if broughtUp(t, w.root, "DIS", disAtt) {
		t.Fatal("a disabled coordinator was woken by a child escalation — reverse activation must respect a human disable")
	}
}

// TestReverseWantsStartLimitDeferred documents that acceptance #3 (a repeated
// wake in a tight cycle trips StartLimit rather than looping) is knowingly not
// implemented in drvctl-041: the StartLimit primitive it asserts against does not
// exist yet. `session.Respawns` is declared but never incremented and there is no
// ceiling that watches it (grep for StartLimit finds only doc references). Loop
// safety for reverse-`wants:` therefore rides on the same, still-to-be-built
// backstop the whole reconcile loop needs — a separate ticket (the drvctl
// StartLimitBurst row), not this one. Kept as a skip so the gap is visible in the
// test file rather than only in the log.
func TestReverseWantsStartLimitDeferred(t *testing.T) {
	t.Skip("StartLimit ceiling + session.Respawns increment are unbuilt; acceptance #3 deferred to the StartLimit primitive ticket (see drvctl-041 log).")
}
