package reconcile_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// --- wants: propagation fixtures (drvctl-038) -------------------------------

// writeSpecWants writes a ticket's spec.md with a `wants:` frontmatter list. The
// ticket dir must already exist (newTicket/newDisabledTicket create it); this only
// layers the edge the propagation reads.
func writeSpecWants(t *testing.T, root store.Root, ticket string, wants ...string) {
	t.Helper()
	if err := root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("---\nid: %s\nwants: [%s]\n---\n\nbody\n", ticket, joinCSV(wants))
	if err := os.WriteFile(root.SpecPath(ticket), []byte(body), 0o644); err != nil {
		t.Fatalf("write spec %s: %v", ticket, err)
	}
}

func joinCSV(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// reconcilerDefaultRepo is reconciler with a `ctl up --repo` fallback set — the
// launch-config default the wants: mint path uses to cut a fresh child attempt.
func (w world) reconcilerDefaultRepo(t *testing.T, f *factory, p *fakeProc, repo string) *reconcile.Reconciler {
	t.Helper()
	r, err := reconcile.New(reconcile.Options{
		Root:        w.root,
		Adapters:    f.adapters,
		Actor:       "agent:claude-code",
		Proc:        p,
		DefaultRepo: repo,
	})
	if err != nil {
		t.Fatalf("reconcile.New: %v", err)
	}
	return r
}

// broughtUp reports whether an attempt has a live session id on record — the
// order-independent "was this attempt admitted?" probe (the desired map's
// iteration order, hence spawn order, is nondeterministic across A/B/CAP).
func broughtUp(t *testing.T, root store.Root, ticket, att string) bool {
	t.Helper()
	sess, err := session.Open(root, ticket, att)
	if err != nil {
		return false
	}
	defer sess.Close()
	id, err := sess.ReadIdentity()
	if err != nil {
		return false
	}
	return id.SessionID != ""
}

// briefAdapter finds the minted fake adapter that was fed a given ticket's
// cold-start brief (nil if none) — the seam that identifies which attempt's
// session a reap hit, independent of the desired map's nondeterministic spawn
// order. The brief header is `# BRIEF <ticket> / attempt …`, so the marker below
// is unambiguous per ticket.
func (f *factory) briefAdapter(ticket string) *fakeAdapter {
	f.mu.Lock()
	defer f.mu.Unlock()
	marker := "BRIEF " + ticket + " "
	for _, a := range f.made {
		for _, p := range a.prompted() {
			if strings.Contains(p, marker) {
				return a
			}
		}
	}
	return nil
}

// --- tests ------------------------------------------------------------------

// TestWantsPropagatesEnableToChildren is the headline acceptance path: enabling a
// parent with `wants: [A, B]` pulls A and B into the fleet, even though neither is
// directly enabled. Desired-ness flows down the edge.
func TestWantsPropagatesEnableToChildren(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	capAtt := w.newTicket(t, "CAP") // enabled + Running
	writeSpecWants(t, w.root, "CAP", "A", "B")
	aAtt := w.newDisabledTicket(t, "A") // Running, NOT enabled
	bAtt := w.newDisabledTicket(t, "B") // Running, NOT enabled

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	waitFor(t, "parent + both children admitted", func() bool { return f.count() == 3 })
	for _, c := range []struct{ ticket, att string }{{"CAP", capAtt}, {"A", aAtt}, {"B", bAtt}} {
		if !broughtUp(t, w.root, c.ticket, c.att) {
			t.Fatalf("%s/%s was not admitted via wants:", c.ticket, c.att)
		}
	}
}

// TestWantsTransitiveToGrandchild: enable flows through an intermediate the parent
// wants (itself only parent-sourced, not directly enabled) down to a grand-child.
func TestWantsTransitiveToGrandchild(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	w.newTicket(t, "CAP") // enabled
	writeSpecWants(t, w.root, "CAP", "MID")
	midAtt := w.newDisabledTicket(t, "MID")
	writeSpecWants(t, w.root, "MID", "LEAF")
	leafAtt := w.newDisabledTicket(t, "LEAF")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	waitFor(t, "grandchild chain admitted", func() bool { return f.count() == 3 })
	if !broughtUp(t, w.root, "MID", midAtt) {
		t.Fatal("MID (child) not admitted")
	}
	if !broughtUp(t, w.root, "LEAF", leafAtt) {
		t.Fatal("LEAF (grand-child) not admitted transitively")
	}
}

// TestWantsWithdrawnOnDisableUnlessDirect: disabling the parent withdraws
// parent-sourced desired-ness (its solely-wanted child is reaped), but a child
// that is *also* directly enabled stays desired and keeps its session.
func TestWantsWithdrawnOnDisableUnlessDirect(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	capAtt := w.newTicket(t, "CAP") // enabled
	writeSpecWants(t, w.root, "CAP", "A", "B")
	w.newDisabledTicket(t, "A") // parent-sourced only
	w.newTicket(t, "B")         // ALSO directly enabled

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	waitFor(t, "all three admitted", func() bool { return f.count() == 3 })
	// Wait for each child's brief to land (injected asynchronously after spawn) so
	// the reap can be attributed to the right attempt.
	var aAdapter, bAdapter *fakeAdapter
	waitFor(t, "child briefs injected", func() bool {
		aAdapter = f.briefAdapter("A")
		bAdapter = f.briefAdapter("B")
		return aAdapter != nil && bAdapter != nil
	})

	// Disable the parent → its parent-sourced desired-ness withdraws.
	if _, err := ticketlog.Append(w.root, "CAP", capAtt, event.Event{Type: "disable", Actor: "human:dave", Body: "park the capability"}); err != nil {
		t.Fatalf("disable CAP: %v", err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("retire tick: %v", err)
	}

	if !aAdapter.wasKilled() {
		t.Fatal("A (parent-sourced only) was not reaped after the parent was disabled")
	}
	if bAdapter.wasKilled() {
		t.Fatal("B (directly enabled) was reaped, but a direct enable must survive the parent's disable")
	}
}

// TestWantsMintsAttemptForChildWithNone: a wanted child with no attempt yet gets
// one minted with the default launch config (the --repo fallback), then admitted —
// the same start a manual enable would give it.
func TestWantsMintsAttemptForChildWithNone(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	w.newTicket(t, "CAP") // enabled
	writeSpecWants(t, w.root, "CAP", "NEW")
	// NEW exists as a ticket (so an attempt can be minted against it) but has no
	// attempt of its own.
	if err := w.root.EnsureTicketDir("NEW"); err != nil {
		t.Fatal(err)
	}

	f := &factory{}
	r := w.reconcilerDefaultRepo(t, f, newProc(), w.repo)
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	ids, err := w.root.ListAttempts("NEW")
	if err != nil {
		t.Fatalf("list NEW attempts: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected exactly one minted attempt for NEW, got %v", ids)
	}
	a, err := project.LoadAttempt(w.root, "NEW", ids[0])
	if err != nil {
		t.Fatalf("load minted attempt: %v", err)
	}
	if a.State != project.Running {
		t.Fatalf("minted attempt should derive Running from its genesis, got %v", a.State)
	}
	if !broughtUp(t, w.root, "NEW", ids[0]) {
		t.Fatal("minted child attempt was not admitted")
	}

	// A second tick must not mint a duplicate — the latest attempt is now Running
	// and is simply re-targeted (fire-once).
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if ids2, _ := w.root.ListAttempts("NEW"); len(ids2) != 1 {
		t.Fatalf("second tick minted a duplicate attempt: %v", ids2)
	}
}

// TestWantsDoesNotRemintTerminalChild: a wanted child whose latest attempt is
// terminal (Done) is left alone — parent-sourced desired-ness neither force-admits
// it nor re-mints over it (auto-freshness is the deferred new-attempt-on-retrigger
// follow-on, not this ticket).
func TestWantsDoesNotRemintTerminalChild(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	w.newTicket(t, "CAP") // enabled
	writeSpecWants(t, w.root, "CAP", "A")
	aAtt := w.newDisabledTicket(t, "A")
	// Close A: a `done` lifecycle event derives it to the terminal Done state.
	if _, err := ticketlog.Append(w.root, "A", aAtt, event.Event{Type: "done", Actor: "human:dave", Body: "closed"}); err != nil {
		t.Fatalf("close A: %v", err)
	}

	f := &factory{}
	r := w.reconcilerDefaultRepo(t, f, newProc(), w.repo)
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	waitFor(t, "parent admitted", func() bool { return f.count() == 1 })

	// Only the parent came up; A neither admitted nor re-minted.
	if ids, _ := w.root.ListAttempts("A"); len(ids) != 1 {
		t.Fatalf("a terminal child was re-minted: %v", ids)
	}
	if broughtUp(t, w.root, "A", aAtt) {
		t.Fatal("a terminal (Done) child was force-admitted by wants: propagation")
	}
	if n := f.count(); n != 1 {
		t.Fatalf("expected only the parent admitted, got %d spawns", n)
	}
}
