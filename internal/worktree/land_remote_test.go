package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit runs a git command in dir with a committer identity, failing the test on
// error. Used to script the "remote forge" side of a MergeRemote scenario.
func runGit(t *testing.T, dir string, args ...string) {
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

// addBareRemote creates a bare repo, adds it to m as remote `name`, and pushes base
// to it — so m's <name>/<base> starts mirroring local base, the state right after
// an attempt was cut from an up-to-date base. Returns the bare repo path.
func addBareRemote(t *testing.T, m *Manager, name, base string) string {
	t.Helper()
	bare := t.TempDir()
	runGit(t, bare, "init", "-q", "--bare", "-b", base)
	runGit(t, m.repo, "remote", "add", name, bare)
	runGit(t, m.repo, "push", "-q", name, base)
	return bare
}

// externalMerge simulates a forge PR ff-merge: it pushes k's branch tip onto the
// remote's base, so after a fetch the branch is contained in <remote>/<base>.
func externalMerge(t *testing.T, m *Manager, remote, base string, k Key) {
	t.Helper()
	runGit(t, m.repo, "push", "-q", remote, k.branch()+":refs/heads/"+base)
}

// TestMergeRemoteContainedRecordsAndPulls is the happy path: the branch landed
// upstream via an external merge, so MergeRemote proves containment and best-effort
// fast-forwards the local base to the fetched remote tip.
func TestMergeRemoteContainedRecordsAndPulls(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	addBareRemote(t, m, "origin", "main")
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	branchTip := revParse(t, wt.Path, "HEAD")
	externalMerge(t, m, "origin", "main", k)

	rm, err := m.MergeRemote(context.Background(), k, "origin", "main")
	if err != nil {
		t.Fatalf("MergeRemote: %v", err)
	}
	if rm.Ref != "refs/remotes/origin/main" {
		t.Errorf("ref = %q, want refs/remotes/origin/main", rm.Ref)
	}
	if rm.BranchTip != branchTip || rm.RemoteTip != branchTip {
		t.Errorf("tips: branch=%s remote=%s, want both %s", rm.BranchTip, rm.RemoteTip, branchTip)
	}
	if !rm.Pull.Moved || rm.Pull.Skipped != "" {
		t.Errorf("expected local main fast-forwarded, got %+v", rm.Pull)
	}
	if got := revParse(t, m.repo, "main"); got != branchTip {
		t.Errorf("local main = %s, want %s", got, branchTip)
	}
}

// TestMergeRemoteNotContainedRefuses proves the load-bearing gate: with no external
// merge the branch is not contained upstream, so MergeRemote refuses and moves
// nothing.
func TestMergeRemoteNotContainedRefuses(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	addBareRemote(t, m, "origin", "main")
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	mainBefore := revParse(t, m.repo, "main")

	_, err := m.MergeRemote(context.Background(), k, "origin", "main")
	if !errors.Is(err, ErrNotContainedUpstream) {
		t.Fatalf("MergeRemote err = %v, want ErrNotContainedUpstream", err)
	}
	if got := revParse(t, m.repo, "main"); got != mainBefore {
		t.Errorf("local main moved on a refused remote merge: %s -> %s", mainBefore, got)
	}
}

// TestMergeRemotePullSkippedOnDivergedBase proves step 4 is best-effort: the branch
// is contained upstream (so the reconcile succeeds), but the local base has its own
// commits and cannot fast-forward, so the pull is skipped with a warning rather
// than failing the reconcile.
func TestMergeRemotePullSkippedOnDivergedBase(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	addBareRemote(t, m, "origin", "main")
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	externalMerge(t, m, "origin", "main", k)
	// Diverge local main independently of the branch.
	writeCommit(t, m.repo, "local.txt", "x", "local-only commit")
	localBefore := revParse(t, m.repo, "main")

	rm, err := m.MergeRemote(context.Background(), k, "origin", "main")
	if err != nil {
		t.Fatalf("MergeRemote: %v", err)
	}
	if !strings.Contains(rm.Pull.Skipped, "diverged") {
		t.Errorf("Pull.Skipped = %q, want a diverged warning", rm.Pull.Skipped)
	}
	if rm.Pull.Moved {
		t.Error("Pull.Moved = true, want a skipped pull")
	}
	if got := revParse(t, m.repo, "main"); got != localBefore {
		t.Errorf("diverged local main changed: %s -> %s", localBefore, got)
	}
}

// TestMergeRemotePullSkippedOnDirtyBase proves a dirty base checkout skips the pull
// with a warning rather than blocking the close.
func TestMergeRemotePullSkippedOnDirtyBase(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	addBareRemote(t, m, "origin", "main")
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	externalMerge(t, m, "origin", "main", k)
	mainBefore := revParse(t, m.repo, "main")
	// Dirty the base checkout (main is checked out in m.repo).
	if err := os.WriteFile(filepath.Join(m.repo, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	rm, err := m.MergeRemote(context.Background(), k, "origin", "main")
	if err != nil {
		t.Fatalf("MergeRemote: %v", err)
	}
	if !strings.Contains(rm.Pull.Skipped, "uncommitted") {
		t.Errorf("Pull.Skipped = %q, want a dirty-base warning", rm.Pull.Skipped)
	}
	if got := revParse(t, m.repo, "main"); got != mainBefore {
		t.Errorf("dirty base main advanced: %s -> %s", mainBefore, got)
	}
}

// TestMergeRemoteAlreadyUpToDate proves the idempotent re-run shape: the branch is
// contained and the local base already carries the remote tip, so the pull reports
// AlreadyUpToDate with nothing to move.
func TestMergeRemoteAlreadyUpToDate(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	addBareRemote(t, m, "origin", "main")
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	externalMerge(t, m, "origin", "main", k)
	// Bring local main up to the branch tip already.
	runGit(t, m.repo, "merge", "--ff-only", k.branch())
	branchTip := revParse(t, wt.Path, "HEAD")

	rm, err := m.MergeRemote(context.Background(), k, "origin", "main")
	if err != nil {
		t.Fatalf("MergeRemote: %v", err)
	}
	if !rm.Pull.Already || rm.Pull.Moved {
		t.Errorf("expected already-up-to-date, got %+v", rm.Pull)
	}
	if got := revParse(t, m.repo, "main"); got != branchTip {
		t.Errorf("local main = %s, want %s", got, branchTip)
	}
}

// TestMergeRemoteMissingBranch refuses (never touching the remote) when the
// attempt's branch does not exist.
func TestMergeRemoteMissingBranch(t *testing.T) {
	m := landManager(t)
	addBareRemote(t, m, "origin", "main")
	_, err := m.MergeRemote(context.Background(), Key{"PROJ-1", "0001"}, "origin", "main")
	if !errors.Is(err, ErrBranchNotFound) {
		t.Fatalf("MergeRemote err = %v, want ErrBranchNotFound", err)
	}
}

// TestRemotesLists returns the configured remotes in git's order.
func TestRemotesLists(t *testing.T) {
	m := landManager(t)
	runGit(t, m.repo, "remote", "add", "origin", "https://example.invalid/o.git")
	runGit(t, m.repo, "remote", "add", "upstream", "https://example.invalid/u.git")
	got, err := m.Remotes(context.Background())
	if err != nil {
		t.Fatalf("Remotes: %v", err)
	}
	if len(got) != 2 || got[0] != "origin" || got[1] != "upstream" {
		t.Errorf("Remotes = %v, want [origin upstream]", got)
	}
}

// TestPushNewBranchSendsTip is the happy path: the attempt branch does not yet
// exist on the remote, so Push creates it and reports NewBranch with the local tip.
func TestPushNewBranchSendsTip(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	bare := addBareRemote(t, m, "origin", "main")
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	tip := revParse(t, wt.Path, "HEAD")

	p, err := m.Push(context.Background(), k, "origin")
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !p.NewBranch || p.UpToDate {
		t.Errorf("flags = {New:%v UpToDate:%v}, want a new branch", p.NewBranch, p.UpToDate)
	}
	if p.Tip != tip {
		t.Errorf("Tip = %s, want local branch tip %s", p.Tip, tip)
	}
	// The branch really reached the remote at the branch tip.
	if got := revParse(t, bare, k.branch()); got != tip {
		t.Errorf("remote %s = %s, want %s", k.branch(), got, tip)
	}
}

// TestPushUpToDateSendsNothing proves the no-op report: pushing the same tip twice
// leaves the second push with UpToDate and no new branch.
func TestPushUpToDateSendsNothing(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	addBareRemote(t, m, "origin", "main")
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	if _, err := m.Push(context.Background(), k, "origin"); err != nil {
		t.Fatalf("first Push: %v", err)
	}

	p, err := m.Push(context.Background(), k, "origin")
	if err != nil {
		t.Fatalf("second Push: %v", err)
	}
	if !p.UpToDate || p.NewBranch {
		t.Errorf("flags = {New:%v UpToDate:%v}, want up to date", p.NewBranch, p.UpToDate)
	}
}

// TestPushRejectsNonFastForward proves Push never forces: if the remote branch has
// advanced independently, the plain push is rejected rather than overwriting it.
func TestPushRejectsNonFastForward(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	bare := addBareRemote(t, m, "origin", "main")
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	if _, err := m.Push(context.Background(), k, "origin"); err != nil {
		t.Fatalf("first Push: %v", err)
	}
	// A second worktree pushes a different tip onto the same remote branch, so the
	// local branch is now behind/diverged from the remote ref.
	other := t.TempDir()
	runGit(t, other, "clone", "-q", bare, ".")
	gitCheckout(t, other, k.branch())
	writeCommit(t, other, "other.txt", "x", "someone else's commit")
	runGit(t, other, "push", "-q", "origin", k.branch())

	_, err := m.Push(context.Background(), k, "origin")
	if err == nil {
		t.Fatal("Push onto a diverged remote branch should be rejected, not forced")
	}
	// The remote still holds the other party's commit — nothing was overwritten.
	if got := revParse(t, bare, k.branch()); got == revParse(t, wt.Path, "HEAD") {
		t.Errorf("remote branch was overwritten to the local tip; a non-ff push must be refused")
	}
}

// TestPushMissingBranch returns ErrBranchNotFound for an attempt with no branch.
func TestPushMissingBranch(t *testing.T) {
	m := landManager(t)
	addBareRemote(t, m, "origin", "main")
	_, err := m.Push(context.Background(), Key{"PROJ-9", "0001"}, "origin")
	if !errors.Is(err, ErrBranchNotFound) {
		t.Fatalf("Push err = %v, want ErrBranchNotFound", err)
	}
}
