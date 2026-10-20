package cmd

import (
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// A leaf ticket with no `wants:` children (and no other wake source in 052) has
// nothing that could wake a parked attempt, so park refuses — nonzero, and the
// log is untouched. drvctl-052 AC3.
func TestParkRefusesWithNoWakeSource(t *testing.T) {
	dir := newTicket(t) // PROJ-1, a leaf
	before, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")

	out, code, err := runE(t, "--data", dir, "--actor", "agent:x", "park", "PROJ-1")
	if code == 0 {
		t.Fatalf("park must refuse a leaf with no wake source, got exit 0: %s", out)
	}
	if err == nil || !strings.Contains(err.Error(), "no pending wake source") {
		t.Errorf("error = %v, want a 'no pending wake source' refusal", err)
	}
	after, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	if len(after) != len(before) {
		t.Errorf("refused park wrote to the log: %d events, was %d", len(after), len(before))
	}
}

// A `wants:` coordinator whose child is still in flight has a pending wake source
// (reverse-`wants:` can still fire), so park succeeds: it writes a durable `park`
// turn-end, exits 0, and leaves the attempt's log state Running (which projects to
// Pending once the session is reaped) — never Needs-me, no escalation. drvctl-052 AC1.
func TestParkSuspendsCoordinatorWithWakeSource(t *testing.T) {
	dir := seedParentChild(t) // PROJ-1 wants PROJ-2 (child escalating, not Done)
	root := store.Root{Dir: dir}

	out, code := run(t, "--data", dir, "--actor", "agent:parent", "park", "PROJ-1", "delegated; nothing needs me")
	if code != 0 {
		t.Fatalf("park exited %d: %s", code, out)
	}
	if !strings.Contains(out, "parked PROJ-1/0001") || !strings.Contains(out, "wants") {
		t.Errorf("park output missing confirmation / wake summary: %q", out)
	}

	events, _ := ticketlog.Read(root, "PROJ-1", "0001")
	last := events[len(events)-1]
	if last.Type != "park" {
		t.Fatalf("last event = %q want park", last.Type)
	}
	if !strings.Contains(last.Body, "delegated; nothing needs me") || !strings.Contains(last.Body, "wants") {
		t.Errorf("park body missing reason / wake source: %q", last.Body)
	}
	// park is not a lifecycle event: the attempt's derived state stays Running (it
	// becomes Pending only via the read-time desired/live projection), and there is
	// no escalation on the parent's own log.
	a, _ := project.LoadAttempt(root, "PROJ-1", "0001")
	if a.State != project.Running {
		t.Errorf("parent log state = %q want Running (park must not be Needs-me/Review)", a.State)
	}
	if len(a.OpenEscalations) != 0 {
		t.Errorf("park raised an escalation on the parent's own log: %+v", a.OpenEscalations)
	}
}

// A coordinator whose every wanted child has reached Done has no future child
// lifecycle to wake on — the coordinator gate admits it for its own final review
// instead — so park refuses it (it should `review`, not sleep). Guards the
// boundary of the wake-source check.
func TestParkRefusesWhenAllChildrenDone(t *testing.T) {
	dir := seedParentChild(t) // PROJ-1 wants PROJ-2
	// Drive the child to Done so the parent's only wake source is gone.
	if _, code := run(t, "--data", dir, "--actor", "human:dave", "resolve", "PROJ-2", "2", "ok"); code != 0 {
		t.Fatal("resolve child escalation failed")
	}
	if _, code := run(t, "--data", dir, "--actor", "human:dave", "done", "PROJ-2"); code != 0 {
		t.Fatal("done child failed")
	}

	out, code, err := runE(t, "--data", dir, "--actor", "agent:parent", "park", "PROJ-1")
	if code == 0 {
		t.Fatalf("park must refuse a coordinator whose children are all Done, got exit 0: %s", out)
	}
	if err == nil || !strings.Contains(err.Error(), "no pending wake source") {
		t.Errorf("error = %v, want a 'no pending wake source' refusal", err)
	}
}

// seedAsyncParked drives a leaf worker's own log to the launch-then-park shape
// `draiver test --detach && draiver park` leaves: a `created`, an `async-job` launch
// marker carrying its target rung, and a `park` behind it — so wake.Pending reports a
// live async-job source the operator can clear.
func seedAsyncParked(t *testing.T, dir string) {
	t.Helper()
	root := store.Root{Dir: dir}
	if _, err := ticketlog.Append(root, "PROJ-1", "0001", event.Event{Type: "async-job", Actor: "agent:x", Rung: "unit", Body: "launched detached test"}); err != nil {
		t.Fatalf("seed async-job: %v", err)
	}
	if _, err := ticketlog.Append(root, "PROJ-1", "0001", event.Event{Type: "park", Actor: "agent:x", Body: "parked behind the detached test"}); err != nil {
		t.Fatalf("seed park: %v", err)
	}
}

// park --clear on an attempt with no pending wake source is the idempotent dual of
// park's no-source refusal: it prints the no-op notice, exits 0, and writes nothing.
// drv-031 AC4.
func TestParkClearNoOpWhenNoSource(t *testing.T) {
	dir := newTicket(t) // PROJ-1, a leaf with no wake source
	root := store.Root{Dir: dir}
	before, _ := ticketlog.Read(root, "PROJ-1", "0001")

	out, code, err := runE(t, "--data", dir, "--actor", "human:dave", "park", "--clear", "PROJ-1")
	if code != 0 || err != nil {
		t.Fatalf("park --clear with no source must exit 0, got code=%d err=%v: %s", code, err, out)
	}
	if !strings.Contains(out, "nothing to clear") || !strings.Contains(out, "no pending wake source") {
		t.Errorf("no-op output missing the notice: %q", out)
	}
	after, _ := ticketlog.Read(root, "PROJ-1", "0001")
	if len(after) != len(before) {
		t.Errorf("no-op clear wrote to the log: %d events, was %d", len(after), len(before))
	}
}

// Clearing an async-job-parked attempt lands a terminal `test-result` with
// Outcome:fail, Refs pointing at the launch marker, the launch's rung copied through,
// and operator provenance — a gate-safe non-verdict that answers the dangling source
// so the reconciler can wake the worker to iterate. drv-031 AC2.
func TestParkClearAsyncJobLandsFailResult(t *testing.T) {
	dir := newTicket(t)
	seedAsyncParked(t, dir) // async-job #2, park #3
	root := store.Root{Dir: dir}

	out, code := run(t, "--data", dir, "--actor", "human:dave", "park", "--clear", "PROJ-1")
	if code != 0 {
		t.Fatalf("park --clear exited %d: %s", code, out)
	}
	if !strings.Contains(out, "cleared async-job source") {
		t.Errorf("output missing the async-job clear confirmation: %q", out)
	}

	events, _ := ticketlog.Read(root, "PROJ-1", "0001")
	last := events[len(events)-1]
	if last.Type != "test-result" {
		t.Fatalf("last event = %q want test-result", last.Type)
	}
	if last.Outcome != "fail" {
		t.Errorf("cleared result Outcome = %q want fail (never readable as green)", last.Outcome)
	}
	if len(last.Refs) != 1 || last.Refs[0] != 2 {
		t.Errorf("cleared result Refs = %v want [2] (the async-job launch marker)", last.Refs)
	}
	if last.Rung != "unit" {
		t.Errorf("cleared result Rung = %q want unit (copied from the launch marker)", last.Rung)
	}
	if !strings.HasPrefix(last.Actor, "human:") {
		t.Errorf("cleared result Actor = %q want operator (human:*) provenance, never agent:*", last.Actor)
	}

	// The source is answered: a second clear is now a no-op, proving no dangling source.
	out2, code2 := run(t, "--data", dir, "--actor", "human:dave", "park", "--clear", "PROJ-1")
	if code2 != 0 || !strings.Contains(out2, "nothing to clear") {
		t.Errorf("after clearing, the source must be answered (second clear a no-op): code=%d %q", code2, out2)
	}
}

// Clearing a `wants`-parked coordinator admits it to assess now: it authors a manual
// reverse-`wants` activation (a `woken-by`) on the coordinator's OWN log with operator
// provenance, even though its children are not all Done. drv-031 AC3.
func TestParkClearWantsActivatesCoordinator(t *testing.T) {
	dir := seedParentChild(t) // PROJ-1 wants PROJ-2 (child escalating, not Done)

	// Park the coordinator behind its wants source, then clear it.
	if _, code := run(t, "--data", dir, "--actor", "agent:parent", "park", "PROJ-1"); code != 0 {
		t.Fatal("park coordinator failed")
	}
	out, code := run(t, "--data", dir, "--actor", "human:dave", "park", "--clear", "PROJ-1")
	if code != 0 {
		t.Fatalf("park --clear wants exited %d: %s", code, out)
	}
	if !strings.Contains(out, "cleared wants source") {
		t.Errorf("output missing the wants clear confirmation: %q", out)
	}

	events, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	last := events[len(events)-1]
	if last.Type != "woken-by" {
		t.Fatalf("last event = %q want woken-by (the manual activation)", last.Type)
	}
	// The activation must sit AFTER the park so the reconciler's active phase re-admits
	// the coordinator; and carry operator provenance.
	var lastPark int
	for _, e := range events {
		if e.Type == "park" {
			lastPark = e.Seq
		}
	}
	if last.Seq <= lastPark {
		t.Errorf("activation seq %d is not after the park seq %d — it would not re-admit", last.Seq, lastPark)
	}
	if !strings.HasPrefix(last.Actor, "human:") {
		t.Errorf("activation Actor = %q want operator (human:*) provenance", last.Actor)
	}
}

// park --clear resolves the same `ticket[@attempt]` positional the rest of the CLI
// takes: the @attempt suffix wins, and a non-existent attempt errors cleanly. drv-031
// AC1.
func TestParkClearResolvesAttemptSuffix(t *testing.T) {
	dir := newTicket(t)
	seedAsyncParked(t, dir)

	if _, code := run(t, "--data", dir, "--actor", "human:dave", "park", "--clear", "PROJ-1@0001"); code != 0 {
		t.Errorf("park --clear PROJ-1@0001 should resolve the attempt and clear, got exit %d", code)
	}
	_, code, err := runE(t, "--data", dir, "--actor", "human:dave", "park", "--clear", "PROJ-1@9999")
	if code == 0 || err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("a non-existent attempt must error cleanly, got code=%d err=%v", code, err)
	}
}

// park without --clear still accepts the @attempt suffix (the only change to the
// normal park path), resolving and suspending the named attempt. drv-031 AC5.
func TestParkAcceptsAttemptSuffix(t *testing.T) {
	dir := seedParentChild(t) // PROJ-1 wants PROJ-2 — a live wants source

	out, code := run(t, "--data", dir, "--actor", "agent:parent", "park", "PROJ-1@0001")
	if code != 0 {
		t.Fatalf("park PROJ-1@0001 exited %d: %s", code, out)
	}
	if !strings.Contains(out, "parked PROJ-1/0001") {
		t.Errorf("park output missing confirmation for the suffixed target: %q", out)
	}
}
