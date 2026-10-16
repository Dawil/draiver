package reconcile_test

import (
	"context"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// --- generalised activation fixtures (drvctl-051) ---------------------------

// review claims review on an attempt (a Review doorbell), the widened trigger
// beyond escalation that lets a manager advance — not only firefight — a child's
// critical path.
func review(t *testing.T, root store.Root, ticket, att string) {
	t.Helper()
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "review", Actor: "agent:x", Body: "claiming review"}); err != nil {
		t.Fatalf("review %s/%s: %v", ticket, att, err)
	}
}

// parkLog appends a `park` hand-off to an attempt's own log — the serial-replay
// stop signal that flips a woken manager's phase from active (doorbell in flight)
// to dormant, so the next tick reaps it to Pending and the one after admits the
// next queued doorbell. Tests append it directly to stand in for the agent running
// `draiver park`.
func parkLog(t *testing.T, root store.Root, ticket, att string) {
	t.Helper()
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "park", Actor: "agent:claude-code", Body: "handled; nothing else needs me"}); err != nil {
		t.Fatalf("park %s/%s: %v", ticket, att, err)
	}
}

// wokenBys returns the `woken-by` activation events the reconciler authored on a
// coordinator's own log, in seq order — the durable record acceptance #1/#2 verify
// the wake against (one per doorbell, naming the child and its doorbell seq).
func wokenBys(t *testing.T, root store.Root, ticket, att string) []event.Event {
	t.Helper()
	events, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		t.Fatalf("read log %s/%s: %v", ticket, att, err)
	}
	var out []event.Event
	for _, e := range events {
		if e.Type == "woken-by" {
			out = append(out, e)
		}
	}
	return out
}

// --- tests ------------------------------------------------------------------

// TestActivationEventAuthoredAtWake is acceptance #2: the reconciler appends a
// `woken-by <child> #<seq>` activation to the **parent's** own log at wake time,
// carrying the machine-readable child/seq payload. It is the newest event on the
// parent's log, so it is the tail of its cold-start brief (the rendering is
// drvctl-053; here we assert the durable event the brief replays).
func TestActivationEventAuthoredAtWake(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	capAtt := w.newTicket(t, "CAP")
	writeSpecWants(t, w.root, "CAP", "CH")
	setSupervision(t, w.root, "CAP", capAtt, project.SupervisionPreDigest)
	chAtt := w.newDisabledTicket(t, "CH")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	waitFor(t, "child admitted", func() bool { return broughtUp(t, w.root, "CH", chAtt) })
	if n := len(wokenBys(t, w.root, "CAP", capAtt)); n != 0 {
		t.Fatalf("a `woken-by` was authored before any doorbell: %d", n)
	}

	// The child escalates (its escalation lands at a known seq on CH's log).
	esc, err := ticketlog.Append(w.root, "CH", chAtt, event.Event{Type: "escalation", Actor: "agent:x", Body: "need a decision"})
	if err != nil {
		t.Fatalf("escalate CH: %v", err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	acts := wokenBys(t, w.root, "CAP", capAtt)
	if len(acts) != 1 {
		t.Fatalf("expected exactly one activation authored at wake, got %d", len(acts))
	}
	a := acts[0]
	if a.WokenChild != "CH" || a.WokenSeq != esc.Seq {
		t.Fatalf("activation payload = (%q, #%d), want (CH, #%d)", a.WokenChild, a.WokenSeq, esc.Seq)
	}
	// It is the last event on the parent's log — the brief tail.
	last, _, err := ticketlog.Last(w.root, "CAP", capAtt)
	if err != nil {
		t.Fatalf("read CAP tail: %v", err)
	}
	if last.Type != "woken-by" {
		t.Fatalf("activation is not the parent's log tail: tail is a %q", last.Type)
	}
}

// TestReviewDoorbellWakesManager is the half of acceptance #1 the old
// escalation-only edge could not do: a child reaching **Review** (not an
// escalation) is a doorbell that wakes the pre-digest manager so it can advance the
// critical path — the widened trigger set (escalation | review | done).
func TestReviewDoorbellWakesManager(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	capAtt := w.newTicket(t, "CAP")
	writeSpecWants(t, w.root, "CAP", "CH")
	setSupervision(t, w.root, "CAP", capAtt, project.SupervisionPreDigest)
	chAtt := w.newDisabledTicket(t, "CH")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	waitFor(t, "child admitted", func() bool { return broughtUp(t, w.root, "CH", chAtt) })

	// CH claims review — no escalation anywhere, yet the manager must wake.
	review(t, w.root, "CH", chAtt)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	waitFor(t, "manager woken by a child Review doorbell", func() bool { return broughtUp(t, w.root, "CAP", capAtt) })

	acts := wokenBys(t, w.root, "CAP", capAtt)
	if len(acts) != 1 || acts[0].WokenChild != "CH" {
		t.Fatalf("expected one Review-doorbell activation naming CH, got %+v", acts)
	}
}

// TestSerialReplayOneDoorbellPerWake is acceptance #3: with N queued doorbells the
// manager is woken N times, oldest-first, one doorbell per wake. The manager parks
// after each, is reaped to Pending, and the next tick admits the next doorbell —
// never a batch. It also proves acceptance #1's "exactly once per doorbell": each
// doorbell yields exactly one activation, in child-log order.
func TestSerialReplayOneDoorbellPerWake(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	capAtt := w.newTicket(t, "CAP")
	writeSpecWants(t, w.root, "CAP", "CH")
	setSupervision(t, w.root, "CAP", capAtt, project.SupervisionPreDigest)
	chAtt := w.newDisabledTicket(t, "CH")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	waitFor(t, "child admitted", func() bool { return broughtUp(t, w.root, "CH", chAtt) })

	// Three doorbells accumulate on CH's log while the manager is dormant, with
	// non-doorbell lifecycle events interleaved (a resolution, a decision) to prove
	// the queue filters to the trigger set. The exact types do not matter beyond
	// *none being `done`*: a `done`-to-all-children would flip CH into the
	// coordinator gate's "admit for final review" path, which is a different wake than
	// the serial doorbell replay under test. The child escalates, is resolved, claims
	// review, is reopened, then re-escalates — three doorbells in seq order.
	d1, err := ticketlog.Append(w.root, "CH", chAtt, event.Event{Type: "escalation", Actor: "agent:x", Body: "blocked"})
	if err != nil {
		t.Fatalf("doorbell 1: %v", err)
	}
	if _, err := ticketlog.Append(w.root, "CH", chAtt, event.Event{Type: "resolution", Actor: "human:dave", Refs: []int{d1.Seq}, Body: "proceed"}); err != nil {
		t.Fatalf("interleaved resolution: %v", err)
	}
	d2, err := ticketlog.Append(w.root, "CH", chAtt, event.Event{Type: "review", Actor: "agent:x", Body: "claiming review"})
	if err != nil {
		t.Fatalf("doorbell 2: %v", err)
	}
	if _, err := ticketlog.Append(w.root, "CH", chAtt, event.Event{Type: "decision", Actor: "human:dave", Body: "reopened"}); err != nil {
		t.Fatalf("interleaved decision: %v", err)
	}
	d3, err := ticketlog.Append(w.root, "CH", chAtt, event.Event{Type: "escalation", Actor: "agent:x", Body: "blocked again"})
	if err != nil {
		t.Fatalf("doorbell 3: %v", err)
	}
	wantOrder := []int{d1.Seq, d2.Seq, d3.Seq}

	// Drain the queue one doorbell per wake. Each round: a tick admits the manager for
	// the next-oldest doorbell (authoring one activation), the manager parks, and the
	// following tick reaps it to Pending — so the round after can admit the next.
	for i := range wantOrder {
		if err := r.Tick(ctx); err != nil {
			t.Fatalf("admit tick for doorbell %d: %v", i+1, err)
		}
		acts := wokenBys(t, w.root, "CAP", capAtt)
		if len(acts) != i+1 {
			t.Fatalf("after admit %d the manager had %d activations, want %d — not one-per-wake", i+1, len(acts), i+1)
		}
		if acts[i].WokenSeq != wantOrder[i] {
			t.Fatalf("activation %d replayed doorbell #%d, want #%d — not oldest-first", i+1, acts[i].WokenSeq, wantOrder[i])
		}
		waitFor(t, "manager admitted for its doorbell", func() bool { return broughtUp(t, w.root, "CAP", capAtt) })

		// The manager hands off, then is reaped so the next doorbell can be admitted.
		parkLog(t, w.root, "CAP", capAtt)
		if err := r.Tick(ctx); err != nil {
			t.Fatalf("reap tick after doorbell %d: %v", i+1, err)
		}
	}

	// All three doorbells drained; a further tick must author no fourth activation.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("drained tick: %v", err)
	}
	if n := len(wokenBys(t, w.root, "CAP", capAtt)); n != 3 {
		t.Fatalf("manager woke %d times for 3 doorbells — queue drained to the wrong count", n)
	}
}

// TestSuppressWhileBlockedThenDrain is acceptance #4: while the manager has an open
// escalation of its own it is not woken by a child doorbell (the doorbell queues
// unhandled); when the human resolves the manager's escalation it returns to
// Running and resumes draining the oldest still-unhandled doorbell.
func TestSuppressWhileBlockedThenDrain(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	capAtt := w.newTicket(t, "CAP")
	writeSpecWants(t, w.root, "CAP", "CH")
	setSupervision(t, w.root, "CAP", capAtt, project.SupervisionPreDigest)
	chAtt := w.newDisabledTicket(t, "CH")

	// The manager carries its own open escalation (Needs-me) — blocked on the human.
	ownEsc, err := ticketlog.Append(w.root, "CAP", capAtt, event.Event{Type: "escalation", Actor: "agent:claude-code", Body: "consolidated: need the prod endpoint"})
	if err != nil {
		t.Fatalf("own escalation: %v", err)
	}

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	// A child doorbell lands while the manager is blocked on the human. (The child is
	// not admitted here — a Needs-me manager is not desired, so it propagates no
	// desiredness down the `wants:` edge; the doorbell simply queues.)
	if _, err := ticketlog.Append(w.root, "CH", chAtt, event.Event{Type: "escalation", Actor: "agent:x", Body: "blocked"}); err != nil {
		t.Fatalf("child doorbell: %v", err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	// Suppressed: no activation authored, manager not admitted — the doorbell queues.
	if n := len(wokenBys(t, w.root, "CAP", capAtt)); n != 0 {
		t.Fatalf("a blocked manager was woken: %d activations authored, want 0", n)
	}
	if broughtUp(t, w.root, "CAP", capAtt) {
		t.Fatal("a manager with its own open escalation was woken by a child doorbell — it must stay blocked on the human")
	}

	// The human resolves the manager's own escalation → it returns to Running.
	if _, err := ticketlog.Append(w.root, "CAP", capAtt, event.Event{Type: "resolution", Actor: "human:dave", Refs: []int{ownEsc.Seq}, Body: "endpoint is X"}); err != nil {
		t.Fatalf("resolve own escalation: %v", err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	// Draining resumes: the still-unhandled child doorbell now wakes the manager.
	waitFor(t, "manager resumes draining after its escalation resolves", func() bool { return broughtUp(t, w.root, "CAP", capAtt) })
	if n := len(wokenBys(t, w.root, "CAP", capAtt)); n != 1 {
		t.Fatalf("expected exactly one activation once unblocked, got %d", n)
	}
}

// TestPassthroughManagerNeverActivates guards that the widened trigger set does not
// leak past the supervision dial: a passthrough coordinator is never woken by a
// review/done doorbell (not only not by an escalation), and no activation is ever
// authored on its log — the child's lifecycle routes to the human, the manager
// stays a Pending shell.
func TestPassthroughManagerNeverActivates(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	capAtt := w.newTicket(t, "CAP") // passthrough (fleet default)
	writeSpecWants(t, w.root, "CAP", "CH")
	chAtt := w.newDisabledTicket(t, "CH")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	waitFor(t, "child admitted", func() bool { return broughtUp(t, w.root, "CH", chAtt) })

	// A Review doorbell (not a `done`, which would be the coordinator gate's final-
	// review admit, a different path) must not wake a passthrough coordinator.
	review(t, w.root, "CH", chAtt)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if n := len(wokenBys(t, w.root, "CAP", capAtt)); n != 0 {
		t.Fatalf("a passthrough coordinator authored %d activations — the dial must gate the widened triggers too", n)
	}
	if broughtUp(t, w.root, "CAP", capAtt) {
		t.Fatal("a passthrough coordinator was woken by a review doorbell — it must stay a Pending shell")
	}
}
