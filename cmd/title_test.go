package cmd

import (
	"os"
	"strings"
	"testing"

	"draiver/internal/store"
)

// specWith writes raw spec.md content for ticket PROJ-1 under a fresh root.
func specWith(t *testing.T, content string) string {
	t.Helper()
	dir := newTicket(t)
	root := store.Root{Dir: dir}
	if err := os.WriteFile(root.SpecPath("PROJ-1"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readSpec(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(store.Root{Dir: dir}.SpecPath("PROJ-1"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTitlePrependsWhenNoFrontmatter(t *testing.T) {
	dir := specWith(t, "Just some prose.\n\n## Goal\n")
	out, code := run(t, "--data", dir, "title", "PROJ-1", "Reorder the board columns")
	if code != 0 {
		t.Fatalf("title exited %d: %s", code, out)
	}
	if !strings.Contains(out, "titled PROJ-1: Reorder the board columns") {
		t.Errorf("unexpected output: %q", out)
	}
	got := readSpec(t, dir)
	want := "---\ntitle: Reorder the board columns\n---\n\nJust some prose.\n\n## Goal\n"
	if got != want {
		t.Errorf("spec = %q want %q", got, want)
	}
}

func TestTitleReplacesInPlace(t *testing.T) {
	dir := specWith(t, "---\nid: PROJ-1\ntitle: PROJ-1\nproject: draiver\n---\n\n# Body\n")
	if _, code := run(t, "--data", dir, "title", "PROJ-1", "Real Title"); code != 0 {
		t.Fatalf("title exited %d", code)
	}
	got := readSpec(t, dir)
	want := "---\nid: PROJ-1\ntitle: Real Title\nproject: draiver\n---\n\n# Body\n"
	if got != want {
		t.Errorf("spec = %q want %q", got, want)
	}
}

func TestTitleRejectsEmpty(t *testing.T) {
	dir := specWith(t, "Prose.\n")
	before := readSpec(t, dir)
	if _, code := run(t, "--data", dir, "title", "PROJ-1", "   "); code == 0 {
		t.Fatal("expected nonzero exit for blank title")
	}
	if readSpec(t, dir) != before {
		t.Error("blank title must not rewrite the spec")
	}
}

func TestTitleUnknownTicket(t *testing.T) {
	dir := newTicket(t)
	if _, code := run(t, "--data", dir, "title", "PROJ-404", "X"); code == 0 {
		t.Fatal("expected nonzero exit for unknown ticket")
	}
}
