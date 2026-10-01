package report

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Dawil/draiver/internal/store"
)

// seedRun writes a cucumber.json under the drv-017 per-run key for an attempt.
func seedRun(t *testing.T, root store.Root, id, att, rung, env, commit, stamp, json string) {
	t.Helper()
	dir := filepath.Join(root.ArtefactsDir(id, att), BDDArtefactSubdir, rung, env, commit, stamp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cucumber.json"), []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverRunsEmpty(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("T-1", "0001"); err != nil {
		t.Fatal(err)
	}
	runs, err := DiscoverRuns(root, "T-1", "0001")
	if err != nil {
		t.Fatalf("DiscoverRuns on empty: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected no runs, got %d", len(runs))
	}
	if _, ok, err := LatestRun(root, "T-1", "0001"); err != nil || ok {
		t.Errorf("LatestRun on empty = ok %v, err %v; want false, nil", ok, err)
	}
}

func TestDiscoverRunsSideBySide(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("T-1", "0001"); err != nil {
		t.Fatal(err)
	}
	// Two regenerations at the same rung/env/commit, plus one at a different commit.
	seedRun(t, root, "T-1", "0001", "integration", "ci", "abc12345", "20260101T090000Z", sample)
	seedRun(t, root, "T-1", "0001", "integration", "ci", "abc12345", "20260101T120000Z", sample)
	seedRun(t, root, "T-1", "0001", "integration", "ci", "def67890", "20260101T080000Z", sample)

	runs, err := DiscoverRuns(root, "T-1", "0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("got %d runs, want 3", len(runs))
	}
	// Newest-first by runstamp.
	if runs[0].Runstamp != "20260101T120000Z" {
		t.Errorf("newest run = %s, want 20260101T120000Z", runs[0].Runstamp)
	}
	// Latest resolves to the newest.
	latest, ok, err := LatestRun(root, "T-1", "0001")
	if err != nil || !ok {
		t.Fatalf("LatestRun = %v, %v", ok, err)
	}
	if latest.Runstamp != "20260101T120000Z" || latest.Rung != "integration" || latest.Env != "ci" {
		t.Errorf("latest = %+v", latest)
	}
	if latest.ShortCommit() != "abc12345" {
		t.Errorf("ShortCommit = %q", latest.ShortCommit())
	}

	// Each run loads and parses its cucumber-JSON.
	rep, err := latest.LoadReport()
	if err != nil {
		t.Fatalf("LoadReport: %v", err)
	}
	if rep.Summarize().Scenarios != 2 {
		t.Errorf("loaded report scenarios = %d, want 2", rep.Summarize().Scenarios)
	}

	// Grouping: two groups (abc×2, def×1); the abc group holds both regenerations.
	groups := GroupRuns(runs)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	var abc *RunGroup
	for i := range groups {
		if groups[i].Commit == "abc12345" {
			abc = &groups[i]
		}
	}
	if abc == nil || len(abc.Runs) != 2 {
		t.Fatalf("abc group = %+v", abc)
	}
	if abc.Runs[0].Runstamp != "20260101T120000Z" {
		t.Errorf("abc group not newest-first: %s", abc.Runs[0].Runstamp)
	}
}

func TestFindRunAndTraversalGuard(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("T-1", "0001"); err != nil {
		t.Fatal(err)
	}
	seedRun(t, root, "T-1", "0001", "unit", "local", "c0ffee", "20260101T100000Z", sample)

	rel := "bdd/unit/local/c0ffee/20260101T100000Z"
	run, ok, err := FindRun(root, "T-1", "0001", rel)
	if err != nil || !ok {
		t.Fatalf("FindRun(%q) = %v, %v", rel, ok, err)
	}
	if run.Rel != rel {
		t.Errorf("found run rel = %q", run.Rel)
	}

	// Bogus and traversal ids resolve to not-found, never escape the tree.
	for _, bad := range []string{
		"bdd/../../../etc/passwd",
		"../../secrets",
		"bdd/nope/nope/nope/nope",
		"",
		"notbdd/unit/local/c0ffee/x",
	} {
		if _, ok, err := FindRun(root, "T-1", "0001", bad); err != nil || ok {
			t.Errorf("FindRun(%q) = ok %v, err %v; want false, nil", bad, ok, err)
		}
	}
}
