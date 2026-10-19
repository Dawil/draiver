package reconcile_test

import (
	"context"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// appendEvt is the async fixtures' tiny log-writer — `test --detach && park` and the
// detached runner's result are all just events on the worker's own log.
func appendEvt(t *testing.T, root store.Root, ticket, att string, e event.Event) {
	t.Helper()
	if _, err := ticketlog.Append(root, ticket, att, e); err != nil {
		t.Fatalf("append %s on %s/%s: %v", e.Type, ticket, att, err)
	}
}

// wokenByOwn finds the `woken-by` activations on an attempt's own log that name its
// OWN ticket as the woken "child" — the async wake the reconciler authors
// (drvctl-055), as opposed to a coordinator wake naming a different child.
func wokenByOwn(t *testing.T, root store.Root, ticket, att string) []event.Event {
	t.Helper()
	events, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		t.Fatalf("read log %s/%s: %v", ticket, att, err)
	}
	var out []event.Event
	for _, e := range events {
		if e.Type == "woken-by" && e.WokenChild == ticket {
			out = append(out, e)
		}
	}
	return out
}

// TestAsyncJobSuspendsThenWakesOnResult is drvctl-055's headline reconcile path: a
// leaf worker that launches a detached test and `park`s is held Pending (trimmed
// from the desired set) while the climb runs, then woken — by the reconciler
// authoring a `woken-by` naming its own ticket — the moment the `test-result` lands
// on its own log (AC #2, #3).
func TestAsyncJobSuspendsThenWakesOnResult(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	att := w.newTicket(t, "WRK") // an enabled, Running leaf worker
	// What `draiver test --detach && draiver park` writes to its own log.
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "async-job", Actor: "agent:x", Rung: "unit", Body: "launched detached test"})
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "park", Actor: "agent:x", Body: "parked behind the detached test"})

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	// Tick 1: parked behind an in-flight detached test ⇒ trimmed from desired, never
	// admitted. It shows Pending (enabled + Running + no live session), not Needs-me.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if broughtUp(t, w.root, "WRK", att) {
		t.Fatal("a worker parked behind an in-flight detached test was admitted — it must suspend to Pending")
	}
	if n := f.count(); n != 0 {
		t.Fatalf("no session should spawn while the detached test runs, got %d", n)
	}
	if got := wokenByOwn(t, w.root, "WRK", att); len(got) != 0 {
		t.Fatalf("no activation should be authored before the result lands, got %d", len(got))
	}

	// The detached runner lands its verdict on the worker's own log.
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "test-result", Actor: "agent:x", Outcome: "pass", Rung: "unit", Commit: "deadbeef", Body: "green"})

	// Tick 2: the result is the doorbell — the reconciler authors the `woken-by` and
	// admits the worker to cold-start and claim review.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	waitFor(t, "worker woken by its own test-result", func() bool { return broughtUp(t, w.root, "WRK", att) })

	got := wokenByOwn(t, w.root, "WRK", att)
	if len(got) != 1 {
		t.Fatalf("want exactly one async woken-by activation, got %d", len(got))
	}

	// Tick 3: the worker is now active (woken-by later than the park) — kept desired,
	// with NO second activation authored (one wake per result).
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if got := wokenByOwn(t, w.root, "WRK", att); len(got) != 1 {
		t.Fatalf("the async wake must be idempotent — want 1 woken-by, got %d", len(got))
	}
}

// TestAsyncJobMidTurnLaunchNotTrimmed guards the live-session boundary: a worker
// that launched a detached test but has NOT parked yet is still mid-turn, so it must
// stay desired — the trim only suspends a worker that has actually parked. Reaping a
// session that is still running would be a regression.
func TestAsyncJobMidTurnLaunchNotTrimmed(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	att := w.newTicket(t, "WRK")
	// Launched, but no `park` yet — the `&&` has not reached park, or the agent is
	// still in the same turn.
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "async-job", Actor: "agent:x", Rung: "unit", Body: "launched"})

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	waitFor(t, "mid-turn worker stays admitted", func() bool { return broughtUp(t, w.root, "WRK", att) })
	if got := wokenByOwn(t, w.root, "WRK", att); len(got) != 0 {
		t.Fatalf("a mid-turn launch (no result) must author no activation, got %d", len(got))
	}
}

// TestAsyncJobIgnoresSyncResult pins the window-walk: a worker with a `test-result`
// that answers NO detached launch (a synchronous `test --log` record) is not an
// async worker, so the pass leaves it entirely alone — it is admitted normally and
// no spurious activation is written.
func TestAsyncJobIgnoresSyncResult(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	att := w.newTicket(t, "WRK")
	// A plain synchronous `test --log` green record, with no async-job launch before it.
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "test-result", Actor: "agent:x", Rung: "unit", Commit: "cafe", Body: "green (sync --log)"})

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	waitFor(t, "worker with only a sync result stays admitted", func() bool { return broughtUp(t, w.root, "WRK", att) })
	if got := wokenByOwn(t, w.root, "WRK", att); len(got) != 0 {
		t.Fatalf("a sync test-result is not an async doorbell — no activation, got %d", len(got))
	}
}
