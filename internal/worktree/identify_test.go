package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRun runs a git command in dir with a fixed identity, failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// TestIdentifyRecoversKeyFromBranch: a checkout on a draiver/<ticket>/<attempt>
// branch identifies as that attempt.
func TestIdentifyRecoversKeyFromBranch(t *testing.T) {
	repo := newRepo(t)
	gitRun(t, repo, "checkout", "-q", "-b", "draiver/PROJ-9/0007")

	got, err := Identify(context.Background(), repo)
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if want := (Key{Ticket: "PROJ-9", Attempt: "0007"}); got != want {
		t.Errorf("Identify = %+v, want %+v", got, want)
	}
}

// TestIdentifyRejectsNonAttemptBranch: a branch outside the managed namespace has
// nothing to attribute, so Identify errors rather than guessing.
func TestIdentifyRejectsNonAttemptBranch(t *testing.T) {
	repo := newRepo(t) // still on main
	if _, err := Identify(context.Background(), repo); err == nil {
		t.Error("expected an error identifying a non-draiver branch")
	}
}

// TestIdentifyRejectsDetachedHead: a detached HEAD is not an attempt checkout.
func TestIdentifyRejectsDetachedHead(t *testing.T) {
	repo := newRepo(t)
	head, err := HeadSHA(context.Background(), repo)
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	gitRun(t, repo, "checkout", "-q", head) // detach
	if _, err := Identify(context.Background(), repo); err == nil {
		t.Error("expected an error identifying a detached HEAD")
	}
}

// TestHeadSHAResolvesFullOid: HeadSHA returns the checkout's full 40-char HEAD oid.
func TestHeadSHAResolvesFullOid(t *testing.T) {
	repo := newRepo(t)
	sha, err := HeadSHA(context.Background(), repo)
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if len(sha) != 40 {
		t.Errorf("HeadSHA = %q (len %d), want a 40-char oid", sha, len(sha))
	}
}

// TestDirtyAtTracksCleanliness: a fresh checkout is clean; an untracked file makes
// it dirty — the guard `test --log` refuses on.
func TestDirtyAtTracksCleanliness(t *testing.T) {
	repo := newRepo(t)
	dirty, err := DirtyAt(context.Background(), repo)
	if err != nil {
		t.Fatalf("DirtyAt: %v", err)
	}
	if dirty {
		t.Error("fresh checkout reported dirty")
	}

	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err = DirtyAt(context.Background(), repo)
	if err != nil {
		t.Fatalf("DirtyAt: %v", err)
	}
	if !dirty {
		t.Error("checkout with an untracked file reported clean")
	}
}
