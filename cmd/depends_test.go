package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
)

// newTicketNamed mints a ticket with a given id under a fresh root and returns
// the data dir; extra ids are created under the same root via addTicket.
func addTicket(t *testing.T, dir, id string) {
	t.Helper()
	if _, code := run(t, "--data", dir, "--actor", "human:test", "new", id, "--title", id, "--repo", dir); code != 0 {
		t.Fatalf("new %s exited %d", id, code)
	}
}

func TestDependsRoundTrip(t *testing.T) {
	dir := newTicket(t) // PROJ-1
	addTicket(t, dir, "PROJ-2")
	addTicket(t, dir, "PROJ-3")

	out, code := run(t, "--data", dir, "depends", "PROJ-1",
		"--wants", "PROJ-2,PROJ-3", "--after", "PROJ-2", "--requires", "PROJ-3")
	if code != 0 {
		t.Fatalf("depends exited %d: %s", code, out)
	}

	// Edges parse back exactly as authored.
	e, err := project.LoadEdges(store.Root{Dir: dir}, "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(e.Wants, ",") != "PROJ-2,PROJ-3" ||
		strings.Join(e.After, ",") != "PROJ-2" ||
		strings.Join(e.Requires, ",") != "PROJ-3" {
		t.Fatalf("round-trip mismatch: %+v", e)
	}
	// The identity frontmatter and body survive the edit.
	spec := readSpec(t, dir)
	if !strings.Contains(spec, "id: PROJ-1") || !strings.Contains(spec, "# Test") {
		t.Errorf("edit clobbered identity/body:\n%s", spec)
	}
}

func TestDependsMergeAdds(t *testing.T) {
	dir := newTicket(t)
	addTicket(t, dir, "PROJ-2")
	addTicket(t, dir, "PROJ-3")

	if _, code := run(t, "--data", dir, "depends", "PROJ-1", "--wants", "PROJ-2"); code != 0 {
		t.Fatal("first depends failed")
	}
	// A second author adds rather than replaces, and de-dupes PROJ-2.
	if _, code := run(t, "--data", dir, "depends", "PROJ-1", "--wants", "PROJ-2,PROJ-3"); code != 0 {
		t.Fatal("second depends failed")
	}
	e, _ := project.LoadEdges(store.Root{Dir: dir}, "PROJ-1")
	if strings.Join(e.Wants, ",") != "PROJ-2,PROJ-3" {
		t.Errorf("merge = %v, want [PROJ-2 PROJ-3]", e.Wants)
	}
}

func TestDependsSelfEdgeRefused(t *testing.T) {
	dir := newTicket(t)
	before := readSpec(t, dir)
	if _, code := run(t, "--data", dir, "depends", "PROJ-1", "--wants", "PROJ-1"); code == 0 {
		t.Fatal("expected nonzero exit for self-edge")
	}
	if readSpec(t, dir) != before {
		t.Error("a refused self-edge must not rewrite the spec")
	}
}

func TestDependsCycleRefused(t *testing.T) {
	dir := newTicket(t)
	addTicket(t, dir, "PROJ-2")

	if _, code := run(t, "--data", dir, "depends", "PROJ-1", "--wants", "PROJ-2"); code != 0 {
		t.Fatal("setup edge failed")
	}
	before := readSpec2(t, dir, "PROJ-2")
	if _, code := run(t, "--data", dir, "depends", "PROJ-2", "--after", "PROJ-1"); code == 0 {
		t.Fatal("expected nonzero exit for cycle")
	}
	if readSpec2(t, dir, "PROJ-2") != before {
		t.Error("a refused cycle must not rewrite the spec")
	}
}

func TestDependsNothingToAuthor(t *testing.T) {
	dir := newTicket(t)
	if _, code := run(t, "--data", dir, "depends", "PROJ-1"); code == 0 {
		t.Fatal("expected nonzero exit when no edge flags given")
	}
}

func TestDependsUnknownTicket(t *testing.T) {
	dir := newTicket(t)
	if _, code := run(t, "--data", dir, "depends", "PROJ-404", "--wants", "PROJ-1"); code == 0 {
		t.Fatal("expected nonzero exit for unknown ticket")
	}
}

func TestDependsPrependsFrontmatter(t *testing.T) {
	// A spec that opens straight into prose gains a frontmatter block.
	dir := specWith(t, "Just prose, no frontmatter.\n")
	if _, code := run(t, "--data", dir, "depends", "PROJ-1", "--wants", "PROJ-2"); code != 0 {
		t.Fatal("depends on prose-only spec failed")
	}
	spec := readSpec(t, dir)
	if !strings.HasPrefix(spec, "---\nwants: [PROJ-2]\n---\n") {
		t.Errorf("expected prepended frontmatter block, got:\n%s", spec)
	}
	if !strings.Contains(spec, "Just prose, no frontmatter.") {
		t.Error("prose body must survive")
	}
}

// TestDependsSetReplaces pins that --set replaces a relation's whole set (adding
// and removing in one edit) rather than unioning — the semantics the web editor
// shells so a deleted id actually leaves the spec.
func TestDependsSetReplaces(t *testing.T) {
	dir := newTicket(t)
	addTicket(t, dir, "PROJ-2")
	addTicket(t, dir, "PROJ-3")

	if _, code := run(t, "--data", dir, "depends", "PROJ-1", "--wants", "PROJ-2,PROJ-3"); code != 0 {
		t.Fatal("setup edges failed")
	}
	// --set wants=PROJ-3 drops PROJ-2 and keeps PROJ-3: a true replace, not a union.
	if _, code := run(t, "--data", dir, "depends", "--set", "PROJ-1", "--wants", "PROJ-3"); code != 0 {
		t.Fatal("depends --set failed")
	}
	e, _ := project.LoadEdges(store.Root{Dir: dir}, "PROJ-1")
	if strings.Join(e.Wants, ",") != "PROJ-3" {
		t.Errorf("set wants = %v, want [PROJ-3]", e.Wants)
	}
}

// TestDependsSetClearsAndLeavesUntouched pins that a blank --set value clears that
// relation while an omitted relation is left exactly as it was.
func TestDependsSetClearsAndLeavesUntouched(t *testing.T) {
	dir := newTicket(t)
	addTicket(t, dir, "PROJ-2")
	addTicket(t, dir, "PROJ-3")

	if _, code := run(t, "--data", dir, "depends", "PROJ-1",
		"--wants", "PROJ-2", "--after", "PROJ-3"); code != 0 {
		t.Fatal("setup edges failed")
	}
	// Clear wants (blank value), pass no --after: wants empties, after survives.
	if _, code := run(t, "--data", dir, "depends", "--set", "PROJ-1", "--wants", ""); code != 0 {
		t.Fatal("depends --set clear failed")
	}
	e, _ := project.LoadEdges(store.Root{Dir: dir}, "PROJ-1")
	if len(e.Wants) != 0 {
		t.Errorf("wants should be cleared, got %v", e.Wants)
	}
	if strings.Join(e.After, ",") != "PROJ-3" {
		t.Errorf("untouched after should survive, got %v", e.After)
	}
}

// TestDependsSetCycleRefused pins that --set is cycle-checked on the resulting set
// and writes nothing when it would close a loop — the refusal the web surfaces.
func TestDependsSetCycleRefused(t *testing.T) {
	dir := newTicket(t)
	addTicket(t, dir, "PROJ-2")

	if _, code := run(t, "--data", dir, "depends", "PROJ-1", "--wants", "PROJ-2"); code != 0 {
		t.Fatal("setup edge failed")
	}
	before := readSpec2(t, dir, "PROJ-2")
	if _, code := run(t, "--data", dir, "depends", "--set", "PROJ-2", "--after", "PROJ-1"); code == 0 {
		t.Fatal("expected nonzero exit for cycle under --set")
	}
	if readSpec2(t, dir, "PROJ-2") != before {
		t.Error("a refused --set cycle must not rewrite the spec")
	}
}

// TestDependsSetNothingToAuthor pins that --set with no relation flag is still an
// error — the clear signal is a *passed* blank flag, not an empty invocation.
func TestDependsSetNothingToAuthor(t *testing.T) {
	dir := newTicket(t)
	if _, code := run(t, "--data", dir, "depends", "--set", "PROJ-1"); code == 0 {
		t.Fatal("expected nonzero exit for --set with no relation flag")
	}
}

// readSpec2 reads an arbitrary ticket's spec.md under dir.
func readSpec2(t *testing.T, dir, ticket string) string {
	t.Helper()
	b, err := os.ReadFile(store.Root{Dir: dir}.SpecPath(ticket))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
