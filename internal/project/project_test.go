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

func TestControl(t *testing.T) {
	cases := []struct {
		name    string
		state   State
		desired bool
		live    bool
		want    State
	}{
		// The Pending case: desired, log-Running, no live agent — the enabled-via-
		// parent-but-not-yet-admitted attempt, and the between-wakes coordinator.
		{"desired running no agent is pending", Running, true, false, Pending},
		// Liveness wins: a live agent is Running, never Pending.
		{"live agent stays running", Running, true, true, Running},
		// Desiredness is required: an undesired Running attempt is a human's to pick
		// up, not the supervisor's to advance — plain Running, not Pending.
		{"undesired running is not pending", Running, false, false, Running},
		{"undesired running with live agent", Running, false, true, Running},
		// Every non-Running state passes through untouched — the overlay only ever
		// turns Running into Pending.
		{"needs-me is never pending", NeedsMe, true, false, NeedsMe},
		{"review is never pending", Review, true, false, Review},
		{"done is never pending", Done, true, false, Done},
		{"done with no agent stays done", Done, true, false, Done},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Control(c.state, c.desired, c.live); got != c.want {
				t.Errorf("Control(%q, desired=%v, live=%v) = %q want %q", c.state, c.desired, c.live, got, c.want)
			}
			// The Attempt.Control() method must agree with the free function.
			a := Attempt{State: c.state, Desired: c.desired, Live: c.live}
			if got := a.Control(); got != c.want {
				t.Errorf("Attempt.Control() = %q want %q", got, c.want)
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

// TestNormalizeType is drv-037 decision #4: absent defaults to the ordinary
// ticket type, the two known values pass through, and anything else is rejected
// (reject-unknown, so a typo fails loudly instead of degrading to a plain ticket).
func TestNormalizeType(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", TypeTicket, false},
		{"   ", TypeTicket, false},
		{"ticket", TypeTicket, false},
		{"codebase", TypeCodebase, false},
		{"  codebase  ", TypeCodebase, false},
		{"code", "", true},    // the spec's original name is not a known value
		{"codbase", "", true}, // a typo
		{"CODEBASE", "", true},
		{"ledger", "", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := NormalizeType(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("NormalizeType(%q) = %q, want error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeType(%q) errored: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("NormalizeType(%q) = %q want %q", c.in, got, c.want)
			}
		})
	}
}

// TestRestingState is drv-037's type-aware control fold: an ordinary ticket's
// derived state is untouched, while a codebase ledger rests Parked (Pending) and
// has no Done — its Running and Done both collapse to Pending, and Needs-me /
// Review pass through so it still surfaces via the normal state machine.
func TestRestingState(t *testing.T) {
	cases := []struct {
		name       string
		state      State
		ticketType string
		want       State
	}{
		{"ticket running stays running", Running, TypeTicket, Running},
		{"ticket done stays done", Done, TypeTicket, Done},
		{"ticket needs-me stays needs-me", NeedsMe, TypeTicket, NeedsMe},
		{"codebase running rests parked", Running, TypeCodebase, Pending},
		{"codebase done is excluded, collapses to parked", Done, TypeCodebase, Pending},
		{"codebase needs-me surfaces via the normal machine", NeedsMe, TypeCodebase, NeedsMe},
		{"codebase review passes through", Review, TypeCodebase, Review},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := restingState(c.state, c.ticketType); got != c.want {
				t.Errorf("restingState(%q, %q) = %q want %q", c.state, c.ticketType, got, c.want)
			}
		})
	}
}

// TestLoadAttemptCodebaseLedger is the end-to-end drv-037 fold: a type: codebase
// spec loads with Type codebase and rests Parked (Pending) even though its log is
// bare (no done/escalation); it is not enabled; an open escalation still surfaces
// Needs-me; and a stray `done` does not close it (no Done transition). An unknown
// type is rejected at load.
func TestLoadAttemptCodebaseLedger(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id := "LEDGER-1"
	if err := root.EnsureAttemptDirs(id, "0001"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath(id), []byte("---\nid: LEDGER-1\ntitle: The Codebase\ntype: codebase\n---\n\n# The Codebase"), 0o644)
	os.WriteFile(root.AttemptMetaPath(id, "0001"), []byte("---\nid: 0001\nticket: LEDGER-1\nrepo: /tmp/x\nbase: main\n---\n"), 0o644)
	ticketlog.Append(root, id, "0001", event.Event{Type: "created", Actor: "a"})

	a, err := LoadAttempt(root, id, "0001")
	if err != nil {
		t.Fatalf("LoadAttempt: %v", err)
	}
	if a.Type != TypeCodebase {
		t.Errorf("Type = %q want %q", a.Type, TypeCodebase)
	}
	if a.State != Pending {
		t.Errorf("resting state = %q want Pending (Parked)", a.State)
	}
	if a.Enabled {
		t.Errorf("a freshly-authored ledger must not be enabled")
	}

	// A stray done must not close a ledger — the Done transition is excluded.
	ticketlog.Append(root, id, "0001", event.Event{Type: "done", Actor: "a", Body: "tried to close"})
	a, err = LoadAttempt(root, id, "0001")
	if err != nil {
		t.Fatal(err)
	}
	if a.State == Done {
		t.Errorf("a codebase ledger reached Done — the transition must be excluded")
	}
	if a.State != Pending {
		t.Errorf("ledger after a stray done = %q want Pending (still Parked)", a.State)
	}

	// An open escalation still surfaces Needs-me via the normal state machine.
	ticketlog.Append(root, id, "0001", event.Event{Type: "escalation", Actor: "a", Body: "which trunk?"})
	a, err = LoadAttempt(root, id, "0001")
	if err != nil {
		t.Fatal(err)
	}
	if a.State != NeedsMe {
		t.Errorf("ledger with open escalation = %q want Needs me", a.State)
	}
}

// TestLoadAttemptRejectsUnknownType: a hand-edited bad type: fails loudly at load,
// exactly as unparseable frontmatter does, rather than silently loading as ordinary.
func TestLoadAttemptRejectsUnknownType(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id := "BAD-1"
	if err := root.EnsureAttemptDirs(id, "0001"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath(id), []byte("---\nid: BAD-1\ntitle: Oops\ntype: codbase\n---\n\n# Oops"), 0o644)
	os.WriteFile(root.AttemptMetaPath(id, "0001"), []byte("---\nid: 0001\nticket: BAD-1\nrepo: /tmp/x\nbase: main\n---\n"), 0o644)
	ticketlog.Append(root, id, "0001", event.Event{Type: "created", Actor: "a"})

	if _, err := LoadAttempt(root, id, "0001"); err == nil {
		t.Fatal("LoadAttempt accepted an unknown ticket type; want reject-unknown error")
	}
}

// TestLoadAttemptDefaultsToTicket: a spec with no type: loads as an ordinary ticket
// and behaves exactly as today — Running resting state, serialisation unaffected.
func TestLoadAttemptDefaultsToTicket(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id := "PLAIN-1"
	if err := root.EnsureAttemptDirs(id, "0001"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath(id), []byte("---\nid: PLAIN-1\ntitle: Plain\n---\n\n# Plain"), 0o644)
	os.WriteFile(root.AttemptMetaPath(id, "0001"), []byte("---\nid: 0001\nticket: PLAIN-1\nrepo: /tmp/x\nbase: main\n---\n"), 0o644)
	ticketlog.Append(root, id, "0001", event.Event{Type: "created", Actor: "a"})

	a, err := LoadAttempt(root, id, "0001")
	if err != nil {
		t.Fatalf("LoadAttempt: %v", err)
	}
	if a.Type != TypeTicket {
		t.Errorf("Type = %q want %q (default)", a.Type, TypeTicket)
	}
	if a.State != Running {
		t.Errorf("ordinary ticket resting state = %q want Running", a.State)
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
