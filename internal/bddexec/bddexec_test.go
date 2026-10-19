package bddexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/report"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

const (
	testTicket = "BDD-1"
	testAtt    = "0001"
)

// gitT runs a git command in dir, failing the test on a nonzero exit.
func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// seedRepo builds a real git repo with the attempt's managed branch/worktree carrying
// a .test-pyramid.yaml and a cucumber report committed on the branch — the shape
// bddexec.Rerun resolves (it cuts a detached worktree at the branch tip and runs the
// rung there). It returns a data root with the attempt's dirs and the base repo path.
//
// cucumberJSON is committed as cucumber.json on the branch so the rung's run can stay a
// trivial `true`; Capture reads it from the (detached) worktree Rerun cuts.
//
// XDG_CACHE_HOME is redirected to a scenario-scoped temp dir so both the managed
// worktree base and the rerun's detached-worktree base live under temp, never the real
// cache. Tests run sequentially, so the env swap is race-free.
func seedRepo(t *testing.T, pyramidYAML, cucumberJSON string) (store.Root, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)

	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "config", "user.email", "t@t")
	gitT(t, repo, "config", "user.name", "t")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	// Cut the attempt's managed worktree + branch, then commit the pyramid and report
	// onto the branch — exactly the "work lives only on the branch" case drv-030 fixes.
	wm, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	key := worktree.Key{Ticket: testTicket, Attempt: testAtt}
	wt, err := wm.Create(context.Background(), worktree.Spec{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, ".test-pyramid.yaml"), []byte(pyramidYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "cucumber.json"), []byte(cucumberJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, wt.Path, "add", "-A")
	gitT(t, wt.Path, "commit", "-q", "-m", "attempt work")

	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs(testTicket, testAtt); err != nil {
		t.Fatal(err)
	}
	return root, repo
}

// branchSHA returns the attempt branch tip — the commit a rerun must stamp its capture
// with (never "nocommit").
func branchSHA(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", "refs/heads/draiver/"+testTicket+"/"+testAtt).Output()
	if err != nil {
		t.Fatalf("rev-parse branch: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func discoverRuns(t *testing.T, root store.Root) []report.Run {
	t.Helper()
	runs, err := report.DiscoverRuns(root, testTicket, testAtt)
	if err != nil {
		t.Fatalf("DiscoverRuns: %v", err)
	}
	return runs
}

func runCount(t *testing.T, root store.Root) int {
	t.Helper()
	return len(discoverRuns(t, root))
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
	root, repo := seedRepo(t, greenPyramid, `[]`)

	out, err := Rerun(context.Background(), root, repo, testTicket, testAtt)
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
	runs := discoverRuns(t, root)
	if len(runs) != 1 {
		t.Fatalf("captured runs = %d, want 1", len(runs))
	}
	// drv-030 AC: the capture is stamped with the real reviewed commit, never "nocommit".
	if want := branchSHA(t, repo); runs[0].Commit != want {
		t.Errorf("captured commit = %q, want the branch tip %q (never nocommit)", runs[0].Commit, want)
	}

	// A second rerun is a fresh side-by-side regeneration, not a clobber.
	if _, err := Rerun(context.Background(), root, repo, testTicket, testAtt); err != nil {
		t.Fatalf("second Rerun: %v", err)
	}
	if n := runCount(t, root); n != 2 {
		t.Fatalf("after second rerun, captured runs = %d, want 2 (side-by-side)", n)
	}
}

// TestRerunRegeneratesForTheReviewedCommit is the heart of drv-030 bug 1: a rerun must
// regenerate the report for the code committed on the attempt's branch — including a
// tagged acceptance feature present only there — not whatever the shared main checkout
// happens to carry. Main holds an untagged report; the branch holds the tagged one;
// the rerun must capture the branch's.
func TestRerunRegeneratesForTheReviewedCommit(t *testing.T) {
	const taggedReport = `[{"uri":"features/manager-signal/board.feature","keyword":"Feature",` +
		`"name":"The board surfaces the manager signal","tags":[{"name":"@drv-030"}],` +
		`"elements":[{"keyword":"Scenario","name":"surfaced","type":"scenario","tags":[{"name":"@drv-030"}],` +
		`"steps":[{"keyword":"Then ","name":"it shows","result":{"status":"passed","duration":1000000}}]}]}]`

	root, repo := seedRepo(t, greenPyramid, taggedReport)

	// The shared main checkout carries a DIFFERENT report with no @drv-030 tag — what the
	// pre-fix rerun (cmd.Dir = main checkout) would have captured.
	if err := os.WriteFile(filepath.Join(repo, "cucumber.json"), []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".test-pyramid.yaml"), []byte(greenPyramid), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Rerun(context.Background(), root, repo, testTicket, testAtt)
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if out.Status != StatusOK {
		t.Fatalf("status = %q, want ok (%v)", out.Status, out.Lines)
	}
	runs := discoverRuns(t, root)
	if len(runs) != 1 {
		t.Fatalf("captured runs = %d, want 1", len(runs))
	}
	rep, err := runs[0].LoadReport()
	if err != nil {
		t.Fatalf("LoadReport: %v", err)
	}
	var sawTaggedFeature bool
	for _, f := range rep.Features {
		if f.Name == "The board surfaces the manager signal" {
			sawTaggedFeature = true
			var sawTag bool
			for _, tg := range f.Tags {
				if tg.Name == "@drv-030" {
					sawTag = true
				}
			}
			if !sawTag {
				t.Errorf("reviewed feature present but its @drv-030 tag did not render")
			}
		}
	}
	if !sawTaggedFeature {
		t.Fatalf("rerun report does not contain the branch-only feature — it ran against the wrong tree")
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
	root, repo := seedRepo(t, redPyramid, `[]`)

	out, err := Rerun(context.Background(), root, repo, testTicket, testAtt)
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
	root, repo := seedRepo(t, envFaultPyramid, `[]`)
	out, err := Rerun(context.Background(), root, repo, testTicket, testAtt)
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
	root, repo := seedRepo(t, noBDDPyramid, `[]`)
	out, err := Rerun(context.Background(), root, repo, testTicket, testAtt)
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if out.Status != StatusNoRung {
		t.Fatalf("status = %q, want %q", out.Status, StatusNoRung)
	}
}

// TestRerunNoBranchErrors: an attempt with no live checkout and no branch has nothing
// to rerun against — a real error, not a silent run against the wrong tree.
func TestRerunNoBranchErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "config", "user.email", "t@t")
	gitT(t, repo, "config", "user.name", "t")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("NOPE-1", "0001"); err != nil {
		t.Fatal(err)
	}
	if _, err := Rerun(context.Background(), root, repo, "NOPE-1", "0001"); err == nil {
		t.Fatal("expected an error when the attempt has neither a checkout nor a branch")
	}
}

func TestBoundBDDRungDetectsRung(t *testing.T) {
	wd := t.TempDir()
	if err := os.WriteFile(filepath.Join(wd, ".test-pyramid.yaml"), []byte(greenPyramid), 0o644); err != nil {
		t.Fatal(err)
	}
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
