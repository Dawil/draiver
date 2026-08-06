package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// landRepo builds a throwaway git repo on `main` with one commit and a committer
// identity, and points the managed-worktree base at a temp dir so the daemon's
// checkouts never touch the real cache. Tests needing git are skipped without it.
func landRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "t@t")
	gitIn(t, dir, "config", "user.name", "t")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) {
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

func commitFile(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", file)
	gitIn(t, dir, "commit", "-q", "-m", "commit "+file)
}

// reviewAttempt sets up the common precondition for a land: a ticket targeting a
// real git repo, its per-attempt branch materialized with a feature commit, and
// the attempt claimed into Review. divergeBase adds an independent commit on the
// base so the branch cannot fast-forward (the sync-first path). It returns the
// data root, the repo path, and the materialized checkout path.
func reviewAttempt(t *testing.T, divergeBase bool) (data, repo, checkout string) {
	t.Helper()
	repo = landRepo(t)
	data = t.TempDir()
	if _, code := run(t, "--data", data, "--actor", "human:test", "new", "PROJ-1", "--title", "T", "--repo", repo); code != 0 {
		t.Fatalf("new exited %d", code)
	}
	// Materialize the branch + worktree the daemon would cut, and put a commit on it.
	m, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	wt, err := m.Create(context.Background(), worktree.Spec{Key: worktree.Key{Ticket: "PROJ-1", Attempt: "0001"}})
	if err != nil {
		t.Fatalf("Create worktree: %v", err)
	}
	commitFile(t, wt.Path, "feat.txt", "feature work")
	if divergeBase {
		commitFile(t, repo, "base.txt", "base moved")
	}
	// review resolves the attempt via --attempt/env/latest (it takes a bare ticket),
	// and 0001 is the only/latest attempt here.
	if _, code := run(t, "--data", data, "--actor", "human:test", "review", "PROJ-1", "ready"); code != 0 {
		t.Fatalf("review exited %d", code)
	}
	return data, repo, wt.Path
}

func stateOf(t *testing.T, data string) project.State {
	t.Helper()
	a, err := project.LoadAttempt(store.Root{Dir: data}, "PROJ-1", "0001")
	if err != nil {
		t.Fatalf("LoadAttempt: %v", err)
	}
	return a.State
}

func headOf(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

func TestCtlMergeLandsAndRecordsDone(t *testing.T) {
	data, repo, checkout := reviewAttempt(t, false)
	featTip := headOf(t, checkout, "HEAD")

	out, code := run(t, "--data", data, "--actor", "human:test", "ctl", "merge", "PROJ-1@0001")
	if code != 0 {
		t.Fatalf("merge exited %d: %s", code, out)
	}
	if !strings.Contains(out, "recorded done") {
		t.Errorf("output missing done confirmation: %s", out)
	}
	if st := stateOf(t, data); st != project.Done {
		t.Errorf("state = %s, want Done", st)
	}
	if got := headOf(t, repo, "main"); got != featTip {
		t.Errorf("main = %s, want feature tip %s", got, featTip)
	}
}

func TestCtlMergeRefusesNonReview(t *testing.T) {
	// A fresh (Running) attempt: no review claim.
	repo := landRepo(t)
	data := t.TempDir()
	if _, code := run(t, "--data", data, "--actor", "human:test", "new", "PROJ-1", "--title", "T", "--repo", repo); code != 0 {
		t.Fatalf("new exited %d", code)
	}
	// A human actor's failure is a plain exit-1 error; the message goes to stderr via
	// Execute (which run bypasses), so assert the code and that nothing landed.
	_, code := run(t, "--data", data, "--actor", "human:test", "ctl", "merge", "PROJ-1@0001")
	if code != 1 {
		t.Fatalf("merge exited %d, want 1", code)
	}
	if st := stateOf(t, data); st != project.Running {
		t.Errorf("state = %s, want Running (nothing recorded)", st)
	}
}

func TestCtlMergeDivergedRefusesThenSyncLands(t *testing.T) {
	data, repo, _ := reviewAttempt(t, true)

	// Plain merge refuses (human actor → plain error, exit 1); nothing lands.
	_, code := run(t, "--data", data, "--actor", "human:test", "ctl", "merge", "PROJ-1@0001")
	if code != 1 {
		t.Fatalf("diverged merge exited %d, want 1", code)
	}
	if st := stateOf(t, data); st != project.Review {
		t.Errorf("state = %s after a refused merge, want Review", st)
	}

	// --sync lands it in one shot.
	out, code := run(t, "--data", data, "--actor", "human:test", "ctl", "merge", "--sync", "PROJ-1@0001")
	if code != 0 {
		t.Fatalf("merge --sync exited %d: %s", code, out)
	}
	if st := stateOf(t, data); st != project.Done {
		t.Errorf("state = %s after merge --sync, want Done", st)
	}
	// main now contains both the base commit and the feature work.
	if _, err := os.Stat(filepath.Join(repo, "feat.txt")); err != nil {
		t.Errorf("feature work not on main after merge --sync: %v", err)
	}
}

func TestCtlMergeDryRunDoesNotMutate(t *testing.T) {
	data, repo, _ := reviewAttempt(t, false)
	mainBefore := headOf(t, repo, "main")

	out, code := run(t, "--data", data, "--actor", "human:test", "ctl", "merge", "--dry-run", "PROJ-1@0001")
	if code != 0 {
		t.Fatalf("dry-run exited %d: %s", code, out)
	}
	if !strings.Contains(out, "ff-landable") {
		t.Errorf("dry-run output = %s", out)
	}
	if got := headOf(t, repo, "main"); got != mainBefore {
		t.Errorf("dry-run moved main: %s -> %s", mainBefore, got)
	}
	if st := stateOf(t, data); st != project.Review {
		t.Errorf("dry-run changed state to %s, want Review", st)
	}
}

// TestCtlMergeFailureDisposition proves the orthogonal failure axis: by default an
// agent actor escalates (durable event → Needs me → exit 3) while a human actor
// gets a plain error (exit 1, no durable event).
func TestCtlMergeFailureDisposition(t *testing.T) {
	t.Run("agent escalates", func(t *testing.T) {
		data, _, _ := reviewAttempt(t, true) // diverged → merge fails
		_, code := run(t, "--data", data, "--actor", "agent:claude", "ctl", "merge", "PROJ-1@0001")
		if code != ExitEscalated {
			t.Fatalf("exit = %d, want %d (escalated)", code, ExitEscalated)
		}
		if st := stateOf(t, data); st != project.NeedsMe {
			t.Errorf("state = %s, want Needs me after an escalation", st)
		}
	})
	t.Run("human errors", func(t *testing.T) {
		data, _, _ := reviewAttempt(t, true)
		_, code := run(t, "--data", data, "--actor", "human:dave", "ctl", "merge", "PROJ-1@0001")
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (plain error)", code)
		}
		if st := stateOf(t, data); st != project.Review {
			t.Errorf("state = %s, want Review (no durable event)", st)
		}
	})
	t.Run("no-escalate overrides agent default", func(t *testing.T) {
		data, _, _ := reviewAttempt(t, true)
		_, code := run(t, "--data", data, "--actor", "agent:claude", "ctl", "merge", "--no-escalate", "PROJ-1@0001")
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (--no-escalate)", code)
		}
		if st := stateOf(t, data); st != project.Review {
			t.Errorf("state = %s, want Review (--no-escalate wrote nothing)", st)
		}
	})
}

func TestCtlSyncBackMergesAndNotes(t *testing.T) {
	data, repo, checkout := reviewAttempt(t, true)

	out, code := run(t, "--data", data, "--actor", "human:test", "ctl", "sync", "PROJ-1@0001")
	if code != 0 {
		t.Fatalf("sync exited %d: %s", code, out)
	}
	// The branch now contains the base commit, so it is ff-landable.
	m, _ := worktree.NewManager(repo)
	mg, err := m.Mergeable(context.Background(), worktree.Key{Ticket: "PROJ-1", Attempt: "0001"}, "main")
	if err != nil || !mg.FastForward {
		t.Errorf("branch not ff-landable after sync (ff=%v err=%v)", mg.FastForward, err)
	}
	// A note records the back-merge; the checkout carries the base file now.
	if _, err := os.Stat(filepath.Join(checkout, "base.txt")); err != nil {
		t.Errorf("base work not synced into the checkout: %v", err)
	}
	a, _ := project.LoadAttempt(store.Root{Dir: data}, "PROJ-1", "0001")
	if a.State != project.Review {
		t.Errorf("sync changed state to %s, want Review (sync is not terminal)", a.State)
	}
}
