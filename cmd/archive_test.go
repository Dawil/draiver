package cmd

import (
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
)

// lastEvent returns the most recent event on PROJ-1/0001.
func lastArchiveEvent(t *testing.T, dir string) (typ, outcome string) {
	t.Helper()
	m, err := project.LoadAttempt(store.Root{Dir: dir}, "PROJ-1", "0001")
	if err != nil {
		t.Fatalf("load attempt: %v", err)
	}
	last := m.Events[len(m.Events)-1]
	return last.Type, last.Outcome
}

// --accepted writes exactly one archive event with outcome accepted, the attempt
// is then Archived and off the board, attributed to the resolved actor.
func TestArchiveAcceptedFlag(t *testing.T) {
	dir := newTicket(t)
	root := store.Root{Dir: dir}

	before, _ := project.LoadAttempt(root, "PROJ-1", "0001")
	out, code := run(t, "--data", dir, "--actor", "human:test", "archive", "PROJ-1", "--accepted")
	if code != 0 {
		t.Fatalf("archive exited %d: %s", code, out)
	}
	after, _ := project.LoadAttempt(root, "PROJ-1", "0001")

	if len(after.Events) != len(before.Events)+1 {
		t.Fatalf("expected exactly one new event, got %d -> %d", len(before.Events), len(after.Events))
	}
	last := after.Events[len(after.Events)-1]
	if last.Type != "archive" || last.Outcome != "accepted" {
		t.Errorf("last event = %q/%q, want archive/accepted", last.Type, last.Outcome)
	}
	if last.Actor != "human:test" {
		t.Errorf("actor = %q, want human:test", last.Actor)
	}
	if !after.Archived {
		t.Error("attempt not Archived after archive")
	}
}

// --abandoned writes outcome abandoned.
func TestArchiveAbandonedFlag(t *testing.T) {
	dir := newTicket(t)
	out, code := run(t, "--data", dir, "archive", "PROJ-1", "--abandoned")
	if code != 0 {
		t.Fatalf("archive exited %d: %s", code, out)
	}
	typ, outcome := lastArchiveEvent(t, dir)
	if typ != "archive" || outcome != "abandoned" {
		t.Errorf("last event = %q/%q, want archive/abandoned", typ, outcome)
	}
}

// With no flag, a fresh (Running) attempt derives the abandoned sentiment; a Done
// attempt derives accepted — mirroring the board's State-derived default.
func TestArchiveStateDerivedDefault(t *testing.T) {
	// Running (fresh ticket) -> abandoned.
	dir := newTicket(t)
	if _, code := run(t, "--data", dir, "archive", "PROJ-1"); code != 0 {
		t.Fatal("archive exited nonzero")
	}
	if typ, outcome := lastArchiveEvent(t, dir); typ != "archive" || outcome != "abandoned" {
		t.Errorf("Running default = %q/%q, want archive/abandoned", typ, outcome)
	}

	// Done -> accepted.
	dir2 := newTicket(t)
	if _, code := run(t, "--data", dir2, "--actor", "human:test", "done", "PROJ-1"); code != 0 {
		t.Fatal("done exited nonzero")
	}
	if _, code := run(t, "--data", dir2, "archive", "PROJ-1"); code != 0 {
		t.Fatal("archive exited nonzero")
	}
	if typ, outcome := lastArchiveEvent(t, dir2); typ != "archive" || outcome != "accepted" {
		t.Errorf("Done default = %q/%q, want archive/accepted", typ, outcome)
	}
}

// --accepted and --abandoned together is rejected before any write.
func TestArchiveMutualExclusion(t *testing.T) {
	dir := newTicket(t)
	root := store.Root{Dir: dir}
	before, _ := project.LoadAttempt(root, "PROJ-1", "0001")

	if _, code := run(t, "--data", dir, "archive", "PROJ-1", "--accepted", "--abandoned"); code == 0 {
		t.Error("expected nonzero exit for --accepted --abandoned")
	}
	after, _ := project.LoadAttempt(root, "PROJ-1", "0001")
	if len(after.Events) != len(before.Events) {
		t.Errorf("mutual-exclusion rejection wrote an event: %d -> %d", len(before.Events), len(after.Events))
	}
}

// Re-archiving an archived attempt is a reported no-op that exits 0 and writes no
// second event; likewise unarchiving a live attempt.
func TestArchiveIdempotentNoOp(t *testing.T) {
	dir := newTicket(t)
	root := store.Root{Dir: dir}

	if _, code := run(t, "--data", dir, "archive", "PROJ-1", "--accepted"); code != 0 {
		t.Fatal("first archive exited nonzero")
	}
	archived, _ := project.LoadAttempt(root, "PROJ-1", "0001")

	out, code := run(t, "--data", dir, "archive", "PROJ-1", "--accepted")
	if code != 0 {
		t.Fatalf("re-archive exited %d, want 0 (no-op)", code)
	}
	if !strings.Contains(out, "no-op") {
		t.Errorf("re-archive output missing no-op notice: %q", out)
	}
	again, _ := project.LoadAttempt(root, "PROJ-1", "0001")
	if len(again.Events) != len(archived.Events) {
		t.Errorf("re-archive wrote a second event: %d -> %d", len(archived.Events), len(again.Events))
	}

	// Unarchiving a live attempt (a fresh ticket) is also a no-op.
	dir2 := newTicket(t)
	out2, code2 := run(t, "--data", dir2, "unarchive", "PROJ-1")
	if code2 != 0 {
		t.Fatalf("unarchive of live attempt exited %d, want 0 (no-op)", code2)
	}
	if !strings.Contains(out2, "no-op") {
		t.Errorf("unarchive no-op output missing notice: %q", out2)
	}
}

// archive -> unarchive round-trips: the attempt leaves and returns to its
// lifecycle-derived column, with one archive then one unarchive event.
func TestArchiveUnarchiveRoundTrip(t *testing.T) {
	dir := newTicket(t)
	root := store.Root{Dir: dir}

	if _, code := run(t, "--data", dir, "archive", "PROJ-1", "--abandoned"); code != 0 {
		t.Fatal("archive exited nonzero")
	}
	if a, _ := project.LoadAttempt(root, "PROJ-1", "0001"); !a.Archived {
		t.Fatal("attempt not Archived after archive")
	}

	out, code := run(t, "--data", dir, "unarchive", "PROJ-1")
	if code != 0 {
		t.Fatalf("unarchive exited %d: %s", code, out)
	}
	a, _ := project.LoadAttempt(root, "PROJ-1", "0001")
	if a.Archived {
		t.Error("attempt still Archived after unarchive")
	}
	if typ, _ := lastArchiveEvent(t, dir); typ != "unarchive" {
		t.Errorf("last event = %q, want unarchive", typ)
	}
}

// A non-existent ticket or attempt errors and writes nothing.
func TestArchiveNonexistentTarget(t *testing.T) {
	dir := newTicket(t)
	if _, code := run(t, "--data", dir, "archive", "NOPE-9"); code == 0 {
		t.Error("expected nonzero exit archiving a non-existent ticket")
	}
	if _, code := run(t, "--data", dir, "archive", "PROJ-1@9999"); code == 0 {
		t.Error("expected nonzero exit archiving a non-existent attempt")
	}
	// The real attempt was never touched.
	if typ, _ := lastArchiveEvent(t, dir); typ == "archive" {
		t.Error("a failed archive wrote to the existing attempt")
	}
}

// The @attempt suffix targets a specific attempt explicitly.
func TestArchiveAttemptSuffix(t *testing.T) {
	dir := newTicket(t)
	out, code := run(t, "--data", dir, "archive", "PROJ-1@0001", "--accepted")
	if code != 0 {
		t.Fatalf("archive with @attempt exited %d: %s", code, out)
	}
	if a, _ := project.LoadAttempt(store.Root{Dir: dir}, "PROJ-1", "0001"); !a.Archived {
		t.Error("attempt not Archived after archive PROJ-1@0001")
	}
}
