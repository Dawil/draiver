package cmd

import (
	"strings"
	"testing"

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
