package reconcile_test

import (
	"context"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/wake"
)

// appendEvt is the async fixtures' tiny log-writer — `test --detach && park` and the
// detached runner's result are all just events on the worker's own log.
func appendEvt(t *testing.T, root store.Root, ticket, att string, e event.Event) {
	t.Helper()
	if _, err := ticketlog.Append(root, ticket, att, e); err != nil {
		t.Fatalf("append %s on %s/%s: %v", e.Type, ticket, att, err)
	}
}

// testResultsOn returns the `test-result` events on an attempt's own log — used to
// assert the liveness-reap landed exactly one terminal fail result (drvctl-057).
func testResultsOn(t *testing.T, root store.Root, ticket, att string) []event.Event {
	t.Helper()
	events, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		t.Fatalf("read log %s/%s: %v", ticket, att, err)
	}
	var out []event.Event
	for _, e := range events {
		if e.Type == "test-result" {
			out = append(out, e)
		}
	}
	return out
}

// launchSeqOn returns the seq of the latest `async-job` launch marker on an
// attempt's own log — the marker a reaped terminal result must Ref. Read rather than
// hardcoded, since the genesis event count a `newTicket` writes is not this test's
// concern.
func launchSeqOn(t *testing.T, root store.Root, ticket, att string) int {
	t.Helper()
	events, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		t.Fatalf("read log %s/%s: %v", ticket, att, err)
	}
	seq := 0
	for _, e := range events {
		if e.Type == "async-job" && e.Seq > seq {
			seq = e.Seq
		}
	}
	return seq
}

// refsContain reports whether an event's Refs include seq — the launch marker a
// reaped/cleared terminal result answers.
func refsContain(refs []int, seq int) bool {
	for _, r := range refs {
		if r == seq {
			return true
		}
	}
	return false
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

// TestAsyncJobClearedLaunchWakesWorker is drv-031's operator-inverse path: when a
// detached runner never lands a verdict and an operator `park --clear`s the stuck
// async-job source, the terminal FAIL result it lands (wake.AsyncLaunchFailResult)
// is just as much a doorbell as a real pass — the reconciler authors the `woken-by`
// and re-admits the worker to iterate. Proves the wake path is outcome-agnostic, so
// the recovery verb hooks into the existing machinery with no new wake code.
func TestAsyncJobClearedLaunchWakesWorker(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	att := w.newTicket(t, "WRK")
	// `draiver test --detach && draiver park`, but the detached runner died: launch +
	// park, no result.
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "async-job", Actor: "agent:x", Rung: "unit", Body: "launched detached test"})
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "park", Actor: "agent:x", Body: "parked behind the detached test"})

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	// Tick 1: parked behind a launch that will never answer ⇒ trimmed, would sleep
	// forever absent the operator inverse.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if broughtUp(t, w.root, "WRK", att) {
		t.Fatal("a worker parked behind an unanswered launch must suspend to Pending")
	}

	// The operator clears it: land the exact terminal fail result `park --clear` writes
	// (the shared drv-031 discipline), answering launch #2.
	cleared := wake.AsyncLaunchFailResult(2, "unit", "operator park --clear")
	cleared.Actor = "human:dave"
	appendEvt(t, w.root, "WRK", att, cleared)

	// Tick 2: the fail result is a doorbell too — the reconciler wakes the worker.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	waitFor(t, "worker woken by the operator-cleared result", func() bool { return broughtUp(t, w.root, "WRK", att) })
	if got := wokenByOwn(t, w.root, "WRK", att); len(got) != 1 {
		t.Fatalf("want exactly one async woken-by activation after the clear, got %d", len(got))
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

// TestAsyncJobReapsDeadRunner is drvctl-057's headline path (AC #1, #2, #3): a
// worker parked behind a detached launch whose runner spawned and then died/hung
// without landing any `test-result` is woken automatically. Once the launch is
// overdue past the reap deadline, the reconciler force-answers it with a terminal
// FAIL result (wake.AsyncLaunchFailResult, Refs → the launch), then the existing
// doorbell path wakes the worker to iterate — the same mechanism as the operator
// `park --clear`, now firing on its own.
func TestAsyncJobReapsDeadRunner(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	att := w.newTicket(t, "WRK")
	// `draiver test --detach && draiver park`, but the runner died mid-climb: launch +
	// park, no result. The launch is stamped an hour ago, so it is well past any
	// realistic acceptance climb.
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "async-job", Actor: "agent:x", Rung: "acceptance", TS: time.Now().Add(-time.Hour), Body: "launched detached test"})
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "park", Actor: "agent:x", Body: "parked behind the detached test"})
	launchSeq := launchSeqOn(t, w.root, "WRK", att)

	f := &factory{}
	r := w.reconcilerReap(t, f, newProc(), 30*time.Minute)
	t.Cleanup(r.Close)

	// Tick 1: the launch is overdue with no result ⇒ reap. The reconciler lands a
	// terminal fail result answering the launch, but leaves the worker trimmed this
	// tick (Pending) — the wake rides the normal doorbell path next tick.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if broughtUp(t, w.root, "WRK", att) {
		t.Fatal("a reaped-but-not-yet-woken worker must still be Pending the tick its launch is reaped")
	}
	reaped := testResultsOn(t, w.root, "WRK", att)
	if len(reaped) != 1 {
		t.Fatalf("want exactly one reaped terminal result, got %d", len(reaped))
	}
	if reaped[0].Outcome != "fail" {
		t.Fatalf("the reaped result must be Outcome fail so the review gate never reads it green, got %q", reaped[0].Outcome)
	}
	if !refsContain(reaped[0].Refs, launchSeq) {
		t.Fatalf("the reaped result must Ref the launch #%d it answers, got refs %v", launchSeq, reaped[0].Refs)
	}
	if reaped[0].Actor != "agent:claude-code" {
		t.Fatalf("the reaped result must carry the reconciler's spine actor, got %q", reaped[0].Actor)
	}
	if got := wokenByOwn(t, w.root, "WRK", att); len(got) != 0 {
		t.Fatalf("no activation should be authored the tick the launch is reaped, got %d", len(got))
	}

	// Tick 2: the reaped fail result is the oldest unhandled async doorbell — the
	// reconciler wakes the worker to cold-start, read the fail, and iterate.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	waitFor(t, "worker woken by the reaped result", func() bool { return broughtUp(t, w.root, "WRK", att) })
	if got := wokenByOwn(t, w.root, "WRK", att); len(got) != 1 {
		t.Fatalf("want exactly one async woken-by after the reap, got %d", len(got))
	}
	// And still exactly one terminal result: the reap is at-most-once per launch.
	if reaped := testResultsOn(t, w.root, "WRK", att); len(reaped) != 1 {
		t.Fatalf("the reap must land at most one terminal result, got %d", len(reaped))
	}
}

// TestAsyncJobNoFalseReapWhileClimbing guards AC #4: a detached runner still
// legitimately climbing (its launch within the deadline) is NOT reaped mid-flight.
// The worker suspends to Pending as usual and waits for the real result — no terminal
// result is fabricated and no activation is written.
func TestAsyncJobNoFalseReapWhileClimbing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	att := w.newTicket(t, "WRK")
	// Launch stamped just now (zero TS ⇒ ticketlog stamps now) + park: a healthy climb
	// mid-flight, comfortably inside the 30-minute deadline.
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "async-job", Actor: "agent:x", Rung: "acceptance", Body: "launched detached test"})
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "park", Actor: "agent:x", Body: "parked behind the detached test"})

	f := &factory{}
	r := w.reconcilerReap(t, f, newProc(), 30*time.Minute)
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if broughtUp(t, w.root, "WRK", att) {
		t.Fatal("a worker mid-climb must stay Pending, not be admitted")
	}
	if reaped := testResultsOn(t, w.root, "WRK", att); len(reaped) != 0 {
		t.Fatalf("a launch within the deadline must not be reaped, got %d terminal result(s)", len(reaped))
	}
	if got := wokenByOwn(t, w.root, "WRK", att); len(got) != 0 {
		t.Fatalf("no activation should be authored for a healthy mid-climb worker, got %d", len(got))
	}
}

// TestAsyncJobReapDisabled pins the 0-disables-the-reaper convention (matching the
// review-env reaper): even a launch long past any deadline is never force-answered
// when AsyncJobReapMaxAge is 0 — a stuck runner then clears only via the manual
// `draiver park --clear`.
func TestAsyncJobReapDisabled(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	att := w.newTicket(t, "WRK")
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "async-job", Actor: "agent:x", Rung: "acceptance", TS: time.Now().Add(-24 * time.Hour), Body: "launched detached test"})
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "park", Actor: "agent:x", Body: "parked behind the detached test"})

	f := &factory{}
	r := w.reconcilerReap(t, f, newProc(), 0) // reaper disabled
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if reaped := testResultsOn(t, w.root, "WRK", att); len(reaped) != 0 {
		t.Fatalf("a disabled reaper must never land a terminal result, got %d", len(reaped))
	}
	if got := wokenByOwn(t, w.root, "WRK", att); len(got) != 0 {
		t.Fatalf("a disabled reaper must author no activation, got %d", len(got))
	}
}

// TestAsyncJobReapThenLateResultWakesOnce guards AC #5's double-result corner: if a
// runner presumed dead is reaped AND then a real result lands too (the runner was
// merely slow), the seq-ordered window-walk treats only the first result after the
// launch as the doorbell, so the worker is woken exactly once — no double woken-by.
func TestAsyncJobReapThenLateResultWakesOnce(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	att := w.newTicket(t, "WRK")
	// launch, park, the reap's fail result, then a late REAL result — two results
	// answering one launch window.
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "async-job", Actor: "agent:x", Rung: "acceptance", Body: "launched detached test"})
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "park", Actor: "agent:x", Body: "parked"})
	reap := wake.AsyncLaunchFailResult(launchSeqOn(t, w.root, "WRK", att), "acceptance", "liveness-reap")
	reap.Actor = "agent:claude-code"
	appendEvt(t, w.root, "WRK", att, reap)
	appendEvt(t, w.root, "WRK", att, event.Event{Type: "test-result", Actor: "agent:x", Outcome: "pass", Rung: "acceptance", Commit: "deadbeef", Body: "the slow runner finally landed green"})

	f := &factory{}
	r := w.reconcilerReap(t, f, newProc(), 30*time.Minute)
	t.Cleanup(r.Close)

	// Several ticks: the wake must settle at exactly one activation regardless of the
	// extra result sitting on the log.
	for i := 0; i < 3; i++ {
		if err := r.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}
	waitFor(t, "worker woken despite two results", func() bool { return broughtUp(t, w.root, "WRK", att) })
	if got := wokenByOwn(t, w.root, "WRK", att); len(got) != 1 {
		t.Fatalf("two results answering one launch must wake the worker exactly once, got %d woken-by", len(got))
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
