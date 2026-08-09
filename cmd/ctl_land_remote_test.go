package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/project"
)

// remoteReviewAttempt builds on reviewAttempt: a Review attempt whose repo has a
// bare `origin` remote mirroring main. With merged=true it also pushes the branch
// tip onto origin/main, simulating a PR merged on the forge. It returns the data
// root, the repo, the checkout, and the per-attempt branch name.
func remoteReviewAttempt(t *testing.T, merged bool) (data, repo, checkout, branch string) {
	t.Helper()
	data, repo, checkout = reviewAttempt(t, false)
	branch = "draiver/PROJ-1/0001"
	bare := t.TempDir()
	gitIn(t, bare, "init", "-q", "--bare", "-b", "main")
	gitIn(t, repo, "remote", "add", "origin", bare)
	gitIn(t, repo, "push", "-q", "origin", "main")
	if merged {
		gitIn(t, repo, "push", "-q", "origin", branch+":refs/heads/main")
	}
	return data, repo, checkout, branch
}

// TestCtlMergeRemoteClosesAndPulls is the happy path: the change landed via an
// external PR, so `ctl merge --remote` records done and fast-forwards local main.
func TestCtlMergeRemoteClosesAndPulls(t *testing.T) {
	data, repo, checkout, _ := remoteReviewAttempt(t, true)
	featTip := headOf(t, checkout, "HEAD")

	out, code := run(t, "--data", data, "--actor", "human:test", "ctl", "merge", "--remote", "PROJ-1@0001")
	if code != 0 {
		t.Fatalf("merge --remote exited %d: %s", code, out)
	}
	if !strings.Contains(out, "recorded done") {
		t.Errorf("output missing done confirmation: %s", out)
	}
	if !strings.Contains(out, "fast-forwarded local main") {
		t.Errorf("output missing pull confirmation: %s", out)
	}
	if st := stateOf(t, data); st != project.Done {
		t.Errorf("state = %s, want Done", st)
	}
	if got := headOf(t, repo, "main"); got != featTip {
		t.Errorf("local main = %s, want feature tip %s (pulled)", got, featTip)
	}
}

// TestCtlMergeRemoteExplicitName targets a specific remote via --remote=NAME.
func TestCtlMergeRemoteExplicitName(t *testing.T) {
	data, _, _, _ := remoteReviewAttempt(t, true)
	out, code := run(t, "--data", data, "--actor", "human:test", "ctl", "merge", "--remote=origin", "PROJ-1@0001")
	if code != 0 {
		t.Fatalf("merge --remote=origin exited %d: %s", code, out)
	}
	if st := stateOf(t, data); st != project.Done {
		t.Errorf("state = %s, want Done", st)
	}
}

// TestCtlMergeRemoteNotContained refuses when the PR was not actually merged: the
// branch is not contained upstream, so nothing is recorded.
func TestCtlMergeRemoteNotContained(t *testing.T) {
	t.Run("human errors", func(t *testing.T) {
		data, repo, _, _ := remoteReviewAttempt(t, false) // no external merge
		mainBefore := headOf(t, repo, "main")
		_, code := run(t, "--data", data, "--actor", "human:test", "ctl", "merge", "--remote", "PROJ-1@0001")
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		if st := stateOf(t, data); st != project.Review {
			t.Errorf("state = %s, want Review (nothing recorded)", st)
		}
		if got := headOf(t, repo, "main"); got != mainBefore {
			t.Errorf("local main moved on a refused remote merge: %s", got)
		}
	})
	t.Run("agent escalates", func(t *testing.T) {
		data, _, _, _ := remoteReviewAttempt(t, false)
		_, code := run(t, "--data", data, "--actor", "agent:claude", "ctl", "merge", "--remote", "PROJ-1@0001")
		if code != ExitEscalated {
			t.Fatalf("exit = %d, want %d (escalated)", code, ExitEscalated)
		}
		if st := stateOf(t, data); st != project.NeedsMe {
			t.Errorf("state = %s, want Needs me", st)
		}
	})
}

// TestCtlMergeRemoteDirtyBaseStillCloses proves step 4 is best-effort: a dirty base
// checkout skips the pull with a warning but still records done.
func TestCtlMergeRemoteDirtyBaseStillCloses(t *testing.T) {
	data, repo, _, _ := remoteReviewAttempt(t, true)
	mainBefore := headOf(t, repo, "main")
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := run(t, "--data", data, "--actor", "human:test", "ctl", "merge", "--remote", "PROJ-1@0001")
	if code != 0 {
		t.Fatalf("merge --remote exited %d: %s", code, out)
	}
	if st := stateOf(t, data); st != project.Done {
		t.Errorf("state = %s, want Done despite a dirty base", st)
	}
	if !strings.Contains(out, "warning") {
		t.Errorf("output missing pull warning: %s", out)
	}
	if got := headOf(t, repo, "main"); got != mainBefore {
		t.Errorf("dirty base main advanced: %s", got)
	}
}

// TestCtlMergeRemoteNoRemote errors clearly when the repo has no git remote.
func TestCtlMergeRemoteNoRemote(t *testing.T) {
	data, _, _ := reviewAttempt(t, false) // no remote added
	_, code := run(t, "--data", data, "--actor", "human:test", "ctl", "merge", "--remote", "PROJ-1@0001")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (no remote)", code)
	}
	if st := stateOf(t, data); st != project.Review {
		t.Errorf("state = %s, want Review (nothing recorded)", st)
	}
}

// TestCtlMergeRemoteAmbiguous refuses when several remotes exist and no NAME or
// primary_remote disambiguates.
func TestCtlMergeRemoteAmbiguous(t *testing.T) {
	data, repo, _, _ := remoteReviewAttempt(t, true)
	gitIn(t, repo, "remote", "add", "upstream", "https://example.invalid/u.git")
	_, code := run(t, "--data", data, "--actor", "human:test", "ctl", "merge", "--remote", "PROJ-1@0001")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (ambiguous remote)", code)
	}
	if st := stateOf(t, data); st != project.Review {
		t.Errorf("state = %s, want Review", st)
	}
	// An explicit name resolves the ambiguity.
	if _, code := run(t, "--data", data, "--actor", "human:test", "ctl", "merge", "--remote=origin", "PROJ-1@0001"); code != 0 {
		t.Fatalf("explicit --remote=origin exited %d, want 0", code)
	}
	if st := stateOf(t, data); st != project.Done {
		t.Errorf("state = %s after --remote=origin, want Done", st)
	}
}

// TestCtlMergeRemoteRejectsDryRun refuses to combine --remote with --dry-run.
func TestCtlMergeRemoteRejectsDryRun(t *testing.T) {
	data, _, _, _ := remoteReviewAttempt(t, true)
	_, code := run(t, "--data", data, "--actor", "human:test", "ctl", "merge", "--remote", "--dry-run", "PROJ-1@0001")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (conflicting flags)", code)
	}
	if st := stateOf(t, data); st != project.Review {
		t.Errorf("state = %s, want Review (nothing recorded)", st)
	}
}
