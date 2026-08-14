package project

import (
	"os"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func ev(seq int, typ string, refs ...int) event.Event {
	return event.Event{Seq: seq, Type: typ, Refs: refs}
}

func TestDerive(t *testing.T) {
	cases := []struct {
		name   string
		events []event.Event
		want   State
		open   int
	}{
		{"empty", nil, Running, 0},
		{"created only", []event.Event{ev(1, "created")}, Running, 0},
		{"working with notes", []event.Event{ev(1, "created"), ev(2, "gotcha"), ev(3, "decision")}, Running, 0},
		{"open escalation", []event.Event{ev(1, "created"), ev(2, "escalation")}, NeedsMe, 1},
		{"resolved escalation returns to running", []event.Event{ev(1, "created"), ev(2, "escalation"), ev(3, "resolution", 2)}, Running, 0},
		{"review claim", []event.Event{ev(1, "created"), ev(2, "review")}, Review, 0},
		{"done is terminal", []event.Event{ev(1, "created"), ev(2, "review"), ev(3, "done")}, Done, 0},
		{"open escalation outranks review", []event.Event{ev(1, "created"), ev(2, "review"), ev(3, "escalation")}, NeedsMe, 1},
		// A decision logged after a review reopens the attempt: Review -> Running.
		{"decision after review reopens to running", []event.Event{ev(1, "created"), ev(2, "review"), ev(3, "decision")}, Running, 0},
		{"decision before review still reaches review", []event.Event{ev(1, "created"), ev(2, "decision"), ev(3, "review")}, Review, 0},
		{"re-review after a reopening decision", []event.Event{ev(1, "created"), ev(2, "review"), ev(3, "decision"), ev(4, "review")}, Review, 0},
		{"open escalation outranks a reopening decision", []event.Event{ev(1, "created"), ev(2, "review"), ev(3, "decision"), ev(4, "escalation")}, NeedsMe, 1},
		{"reopened after resolution", []event.Event{ev(1, "created"), ev(2, "escalation"), ev(3, "resolution", 2), ev(4, "escalation")}, NeedsMe, 1},
		{"two escalations one resolved", []event.Event{ev(1, "escalation"), ev(2, "escalation"), ev(3, "resolution", 1)}, NeedsMe, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, open := Derive(c.events)
			if got != c.want {
				t.Errorf("state = %q want %q", got, c.want)
			}
			if len(open) != c.open {
				t.Errorf("open escalations = %d want %d", len(open), c.open)
			}
		})
	}
}

func TestDeriveEnabled(t *testing.T) {
	cases := []struct {
		name   string
		events []event.Event
		want   bool
	}{
		{"default is disabled", []event.Event{ev(1, "created")}, false},
		{"empty is disabled", nil, false},
		{"enable turns it on", []event.Event{ev(1, "created"), ev(2, "enable")}, true},
		{"disable after enable turns it off", []event.Event{ev(1, "created"), ev(2, "enable"), ev(3, "disable")}, false},
		{"re-enable after disable", []event.Event{ev(1, "enable"), ev(2, "disable"), ev(3, "enable")}, true},
		{"other events do not enable", []event.Event{ev(1, "created"), ev(2, "gotcha"), ev(3, "review")}, false},
		{"last write wins", []event.Event{ev(1, "disable"), ev(2, "enable"), ev(3, "disable"), ev(4, "enable")}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeriveEnabled(c.events); got != c.want {
				t.Errorf("DeriveEnabled = %v want %v", got, c.want)
			}
		})
	}
}

func TestDeriveArchived(t *testing.T) {
	cases := []struct {
		name   string
		events []event.Event
		want   bool
	}{
		{"default is un-archived", []event.Event{ev(1, "created")}, false},
		{"empty is un-archived", nil, false},
		{"archive takes it off the board", []event.Event{ev(1, "created"), ev(2, "archive")}, true},
		{"unarchive after archive brings it back", []event.Event{ev(1, "created"), ev(2, "archive"), ev(3, "unarchive")}, false},
		{"re-archive after unarchive", []event.Event{ev(1, "archive"), ev(2, "unarchive"), ev(3, "archive")}, true},
		{"other events do not archive", []event.Event{ev(1, "created"), ev(2, "done"), ev(3, "enable")}, false},
		{"last write wins", []event.Event{ev(1, "archive"), ev(2, "unarchive"), ev(3, "archive"), ev(4, "unarchive")}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeriveArchived(c.events); got != c.want {
				t.Errorf("DeriveArchived = %v want %v", got, c.want)
			}
		})
	}
}

// TestArchiveDoesNotChangeState: archive is board-membership, a separate axis; an
// archive/unarchive event must never move an attempt off its lifecycle state. A
// Done attempt stays Done when archived (its bit is orthogonal).
func TestArchiveDoesNotChangeState(t *testing.T) {
	events := []event.Event{ev(1, "created"), ev(2, "done"), ev(3, "archive"), ev(4, "unarchive")}
	if got, _ := Derive(events); got != Done {
		t.Errorf("state = %q want Done (archive/unarchive are not lifecycle)", got)
	}
}

// TestEnableDisableDoNotChangeState: enablement is a separate axis; enable/disable
// events must never move an attempt off Running.
func TestEnableDisableDoNotChangeState(t *testing.T) {
	events := []event.Event{ev(1, "created"), ev(2, "enable"), ev(3, "disable")}
	if got, _ := Derive(events); got != Running {
		t.Errorf("state = %q want Running (enable/disable are not lifecycle)", got)
	}
}

func TestLoadAllReturnsOneCardPerAttemptWithIndependentState(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id := "PROJ-1"
	for _, a := range []string{"0001", "0002"} {
		if err := root.EnsureAttemptDirs(id, a); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(root.SpecPath(id), []byte("---\nid: PROJ-1\ntitle: Auth\n---\n\n# Auth"), 0o644)

	// 0001 -> Needs me (open escalation); 0002 -> Review.
	ticketlog.Append(root, id, "0001", event.Event{Type: "created", Actor: "a"})
	ticketlog.Append(root, id, "0001", event.Event{Type: "escalation", Actor: "a", Body: "blocked"})
	ticketlog.Append(root, id, "0002", event.Event{Type: "created", Actor: "a"})
	ticketlog.Append(root, id, "0002", event.Event{Type: "review", Actor: "a", Body: "done"})

	attempts, err := LoadAll(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("got %d attempts want 2", len(attempts))
	}
	byID := map[string]Attempt{}
	for _, a := range attempts {
		byID[a.ID] = a
	}
	if byID["0001"].State != NeedsMe {
		t.Errorf("0001 state = %q want Needs me", byID["0001"].State)
	}
	if byID["0002"].State != Review {
		t.Errorf("0002 state = %q want Review", byID["0002"].State)
	}
	if byID["0001"].Title != "Auth" {
		t.Errorf("title not loaded from shared spec: %q", byID["0001"].Title)
	}
}
