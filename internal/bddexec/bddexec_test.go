package bddexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/report"
	"github.com/Dawil/draiver/internal/store"
)

// seedWorktree builds a temp worktree holding a .test-pyramid.yaml and a cucumber
// report the BDD rung's capture will pick up, plus a data root with a live attempt.
// The worktree is deliberately not a git repo — HeadSHA then falls back to a
// "nocommit" commit segment, which is all the capture needs.
func seedWorktree(t *testing.T, pyramidYAML string) (store.Root, string) {
	t.Helper()
	wd := t.TempDir()
	if err := os.WriteFile(filepath.Join(wd, ".test-pyramid.yaml"), []byte(pyramidYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	// A cucumber-JSON the rung "produces" — pre-placed so the run command can stay a
	// trivial `true`; Capture reads it from the worktree regardless.
	if err := os.WriteFile(filepath.Join(wd, "cucumber.json"), []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("BDD-1", "0001"); err != nil {
		t.Fatal(err)
	}
	return root, wd
}

func runCount(t *testing.T, root store.Root) int {
	t.Helper()
	runs, err := report.DiscoverRuns(root, "BDD-1", "0001")
	if err != nil {
		t.Fatalf("DiscoverRuns: %v", err)
	}
	return len(runs)
}

const greenPyramid = `environments:
  - name: local
    healthchecks:
      - name: ready
        script: "true"
levels:
  - name: unit
    run: "true"
  - name: bdd
    run: "true"
    environment: local
    cucumber_json: cucumber.json
`

func TestRerunGreenCapturesASideBySideRun(t *testing.T) {
	root, wd := seedWorktree(t, greenPyramid)

	out, err := Rerun(context.Background(), root, wd, "BDD-1", "0001")
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if out.Status != StatusOK {
		t.Fatalf("status = %q, want %q (lines: %v)", out.Status, StatusOK, out.Lines)
	}
	if out.RunKey == "" {
		t.Error("green rerun captured no run key")
	}
	if out.Rung != "bdd" {
		t.Errorf("rung = %q, want bdd", out.Rung)
	}
	if n := runCount(t, root); n != 1 {
		t.Fatalf("captured runs = %d, want 1", n)
	}

	// A second rerun is a fresh side-by-side regeneration, not a clobber.
	if _, err := Rerun(context.Background(), root, wd, "BDD-1", "0001"); err != nil {
		t.Fatalf("second Rerun: %v", err)
	}
	if n := runCount(t, root); n != 2 {
		t.Fatalf("after second rerun, captured runs = %d, want 2 (side-by-side)", n)
	}
}

const redPyramid = `environments:
  - name: local
    healthchecks:
      - name: ready
        script: "false"
levels:
  - name: bdd
    run: "true"
    environment: local
    cucumber_json: cucumber.json
`

func TestRerunRedHealthcheckBlocksWithoutCapturing(t *testing.T) {
	root, wd := seedWorktree(t, redPyramid)

	out, err := Rerun(context.Background(), root, wd, "BDD-1", "0001")
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if out.Status != StatusBlocked {
		t.Fatalf("status = %q, want %q", out.Status, StatusBlocked)
	}
	if out.RunKey != "" {
		t.Errorf("blocked rerun must capture nothing, got run key %q", out.RunKey)
	}
	if n := runCount(t, root); n != 0 {
		t.Errorf("blocked rerun captured %d run(s), want 0 (no bogus report)", n)
	}
	// The banner must explain, in the drv-012 "pass the ball" language.
	joined := strings.Join(out.Lines, "\n")
	if !strings.Contains(joined, "blocked") || !strings.Contains(joined, "Pass the ball") {
		t.Errorf("blocked banner does not explain / pass the ball:\n%s", joined)
	}
}

const envFaultPyramid = `environments:
  - name: local
    up: "false"
    healthchecks:
      - name: ready
        script: "true"
levels:
  - name: bdd
    run: "true"
    environment: local
    cucumber_json: cucumber.json
`

func TestRerunEnvUpFaultIsInfrastructure(t *testing.T) {
	root, wd := seedWorktree(t, envFaultPyramid)
	out, err := Rerun(context.Background(), root, wd, "BDD-1", "0001")
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if out.Status != StatusEnvFault {
		t.Fatalf("status = %q, want %q", out.Status, StatusEnvFault)
	}
	if n := runCount(t, root); n != 0 {
		t.Errorf("env-fault rerun captured %d run(s), want 0", n)
	}
}

const noBDDPyramid = `levels:
  - name: unit
    run: "true"
`

func TestRerunNoBDDRung(t *testing.T) {
	root, wd := seedWorktree(t, noBDDPyramid)
	out, err := Rerun(context.Background(), root, wd, "BDD-1", "0001")
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if out.Status != StatusNoRung {
		t.Fatalf("status = %q, want %q", out.Status, StatusNoRung)
	}
}

func TestBoundBDDRungDetectsRung(t *testing.T) {
	_, wd := seedWorktree(t, greenPyramid)
	lv, env, ok, err := BoundBDDRung(wd)
	if err != nil {
		t.Fatalf("BoundBDDRung: %v", err)
	}
	if !ok {
		t.Fatal("expected a BDD rung to be detected")
	}
	if lv.Name != "bdd" {
		t.Errorf("rung = %q, want bdd", lv.Name)
	}
	if env == nil || env.Name != "local" {
		t.Errorf("env = %v, want local", env)
	}

	// No pyramid at all → no rung, no error.
	empty := t.TempDir()
	if _, _, ok, err := BoundBDDRung(empty); err != nil || ok {
		t.Errorf("BoundBDDRung(no pyramid) = ok %v err %v, want false nil", ok, err)
	}
}
