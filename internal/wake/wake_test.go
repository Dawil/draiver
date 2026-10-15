package wake_test

import (
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/wake"
)

// seedTicket writes ticket's spec.md (carrying the optional `wants:` edges) and
// creates one running attempt, returning its id. With no wants it is a leaf; with
// wants it is a coordinator whose children's lifecycle can wake it.
func seedTicket(t *testing.T, root store.Root, ticket string, wants ...string) string {
	t.Helper()
	if err := root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	fm := "---\nid: " + ticket + "\n"
	if len(wants) > 0 {
		fm += "wants: [" + strings.Join(wants, ", ") + "]\n"
	}
	fm += "---\n\nbody\n"
	if err := os.WriteFile(root.SpecPath(ticket), []byte(fm), 0o644); err != nil {
		t.Fatalf("write spec %s: %v", ticket, err)
	}
	m, err := attempt.Create(root, ticket, attempt.New{Tool: "claude-code", Model: "opus-4.8", Repo: "/repo", Actor: "human:x"})
	if err != nil {
		t.Fatalf("create attempt %s: %v", ticket, err)
	}
	return m.ID
}

// reachDone drives an attempt to Done with a terminal lifecycle event, so it drops
// out of a coordinator's set of in-flight children.
func reachDone(t *testing.T, root store.Root, ticket, att string) {
	t.Helper()
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "done", Actor: "human:dave", Body: "closed"}); err != nil {
		t.Fatalf("done %s/%s: %v", ticket, att, err)
	}
}

// A leaf ticket — no `wants:` edges — has no wake source in drvctl-052 (its own
// async-job source is drvctl-055), so Pending is empty: park must refuse it and
// the completion gate must not treat its park as a hand-off.
func TestPendingLeafHasNoSource(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	att := seedTicket(t, root, "LEAF")

	sources, err := wake.Pending(root, "LEAF", att)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(sources) != 0 {
		t.Fatalf("a leaf has no wake source in 052, got %+v", sources)
	}
}

// A `wants:` coordinator with at least one child still in flight (not Done) has a
// pending wake source: reverse-`wants:` activation can still fire on that child's
// lifecycle, so the attempt can be woken and park is legitimate. The source names
// only the in-flight children, not those already Done.
func TestPendingCoordinatorWithInflightChild(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	parentAtt := seedTicket(t, root, "CAP", "C1", "C2")
	c1 := seedTicket(t, root, "C1")
	seedTicket(t, root, "C2")
	reachDone(t, root, "C1", c1) // C1 Done; C2 still in flight → still wakeable

	sources, err := wake.Pending(root, "CAP", parentAtt)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(sources) != 1 || sources[0].Kind != wake.Wants {
		t.Fatalf("want a single wants source, got %+v", sources)
	}
	if !strings.Contains(sources[0].Desc, "C2") {
		t.Errorf("source should name the in-flight child C2: %q", sources[0].Desc)
	}
	if strings.Contains(sources[0].Desc, "C1") {
		t.Errorf("source must not name the Done child C1: %q", sources[0].Desc)
	}
}

// A coordinator whose every wanted child has reached Done has no future child
// lifecycle to wake on — the coordinator gate admits it for its own final review
// rather than parking it — so Pending is empty. This is the upper boundary of the
// wake-source window, mirrored by cmd's TestParkRefusesWhenAllChildrenDone.
func TestPendingCoordinatorAllChildrenDone(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	parentAtt := seedTicket(t, root, "CAP", "C1")
	c1 := seedTicket(t, root, "C1")
	reachDone(t, root, "C1", c1)

	sources, err := wake.Pending(root, "CAP", parentAtt)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(sources) != 0 {
		t.Fatalf("all children Done ⇒ no wake source (review, don't park), got %+v", sources)
	}
}
