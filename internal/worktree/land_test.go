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

// landManager is newManager plus a committer identity in the repo config, so the
// Manager's own git calls (a sync's merge commit) have an author without relying
// on ambient GIT_AUTHOR_* env. The config lives in the shared common dir, so every
// linked worktree inherits it.
func landManager(t *testing.T) *Manager {
	t.Helper()
	m := newManager(t)
	gitConfig(t, m.repo, "user.email", "t@t")
	gitConfig(t, m.repo, "user.name", "t")
	return m
}

func gitConfig(t *testing.T, dir, key, val string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", dir, "config", key, val).CombinedOutput(); err != nil {
		t.Fatalf("git config %s: %v\n%s", key, err, out)
	}
}

// writeCommit writes file=content under dir and commits it, so tests can diverge
// two branches on the same or different paths at will.
func writeCommit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", file}, {"commit", "-q", "-m", msg}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
}

func gitCheckout(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir, "checkout", "-q"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git checkout %v: %v\n%s", args, err, out)
	}
}

// TestMergeFastForwardIntoCheckedOutBase lands a branch that is strictly ahead of
// its base while base is checked out in the main repo: the base ref and its
// working tree both advance to the branch tip.
func TestMergeFastForwardIntoCheckedOutBase(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	featTip := revParse(t, wt.Path, "HEAD")

	landed, err := m.Merge(context.Background(), k, "main")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if landed.AlreadyUpToDate {
		t.Error("expected a real land, got AlreadyUpToDate")
	}
	if landed.Tip != featTip {
		t.Errorf("landed tip = %s, want feature tip %s", landed.Tip, featTip)
	}
	if got := revParse(t, m.repo, "main"); got != featTip {
		t.Errorf("main = %s, want %s", got, featTip)
	}
	// The main repo's working tree advanced too: the feature file is present.
	if _, err := os.Stat(filepath.Join(m.repo, "feat.txt")); err != nil {
		t.Errorf("main working tree did not advance: %v", err)
	}
}

// TestMergeFastForwardIntoUnCheckedOutBase lands when base is checked out nowhere:
// the ref is moved by a compare-and-swap update-ref, leaving no working tree to
// disturb.
func TestMergeFastForwardIntoUnCheckedOutBase(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	featTip := revParse(t, wt.Path, "HEAD")
	// Move the main repo off `main` so base is checked out nowhere.
	gitCheckout(t, m.repo, "-b", "sidebar")

	if _, err := m.Merge(context.Background(), k, "main"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got := revParse(t, m.repo, "main"); got != featTip {
		t.Errorf("main ref = %s, want %s", got, featTip)
	}
}

// TestMergeDivergedRefuses proves the ff-only contract: a branch whose base has
// moved independently cannot be landed, and nothing is mutated.
func TestMergeDivergedRefuses(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	writeCommit(t, m.repo, "base.txt", "moved", "base moved") // main diverges
	mainBefore := revParse(t, m.repo, "main")

	_, err := m.Merge(context.Background(), k, "main")
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("Merge err = %v, want ErrDiverged", err)
	}
	if got := revParse(t, m.repo, "main"); got != mainBefore {
		t.Errorf("main moved on a refused merge: %s -> %s", mainBefore, got)
	}
}

// TestSyncThenMergeLands is the divergence path the spec mandates instead of
// rebase: sync back-merges base into the branch (additively, SHAs preserved),
// which flips the ancestry so the branch becomes ff-landable.
func TestSyncThenMergeLands(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	featCommit := revParse(t, wt.Path, "HEAD")
	writeCommit(t, m.repo, "base.txt", "moved", "base moved") // clean divergence (different file)

	synced, err := m.Sync(context.Background(), k, "main")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if synced.AlreadyUpToDate {
		t.Error("expected a real sync, got AlreadyUpToDate")
	}
	// Additive: the original feature commit is still reachable with its SHA intact.
	if !isReachable(t, wt.Path, featCommit) {
		t.Errorf("sync rewrote history: %s no longer reachable", featCommit)
	}
	// The branch tip is a merge commit (two parents).
	if parents := revList(t, wt.Path, "HEAD", "--max-count=1", "--pretty=%P"); len(strings.Fields(parents)) != 2 {
		t.Errorf("tip is not a merge commit; parents = %q", parents)
	}

	// Now ff-landable.
	landed, err := m.Merge(context.Background(), k, "main")
	if err != nil {
		t.Fatalf("Merge after sync: %v", err)
	}
	if got := revParse(t, m.repo, "main"); got != landed.Tip {
		t.Errorf("main = %s, want landed tip %s", got, landed.Tip)
	}
}

// TestSyncConflictAborts proves a conflicting sync restores the pre-sync state
// rather than leaving a half-merged checkout.
func TestSyncConflictAborts(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	writeCommit(t, wt.Path, "clash.txt", "feature side", "feature edit")
	featTip := revParse(t, wt.Path, "HEAD")
	writeCommit(t, m.repo, "clash.txt", "base side", "base edit") // same path, conflicting

	_, err := m.Sync(context.Background(), k, "main")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Sync err = %v, want ErrConflict", err)
	}
	if got := revParse(t, wt.Path, "HEAD"); got != featTip {
		t.Errorf("tip moved on a conflicting sync: %s -> %s", featTip, got)
	}
	// No merge left in progress, and the checkout is clean.
	if dirty, _ := m.dirtyAt(context.Background(), wt.Path); dirty {
		t.Error("checkout left dirty after an aborted sync")
	}
}

// TestMergeableDryRun covers the three predictions merge-tree + is-ancestor make,
// with no mutation.
func TestMergeableDryRun(t *testing.T) {
	t.Run("fast-forward", func(t *testing.T) {
		m := landManager(t)
		k := Key{"PROJ-1", "0001"}
		wt := mustCreate(t, m, k.Ticket, k.Attempt)
		writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
		mg, err := m.Mergeable(context.Background(), k, "main")
		if err != nil {
			t.Fatalf("Mergeable: %v", err)
		}
		if !mg.FastForward || !mg.Clean() {
			t.Errorf("ff case: %+v", mg)
		}
	})
	t.Run("diverged-clean", func(t *testing.T) {
		m := landManager(t)
		k := Key{"PROJ-1", "0001"}
		wt := mustCreate(t, m, k.Ticket, k.Attempt)
		writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
		writeCommit(t, m.repo, "base.txt", "moved", "base moved")
		mg, err := m.Mergeable(context.Background(), k, "main")
		if err != nil {
			t.Fatalf("Mergeable: %v", err)
		}
		if mg.FastForward || !mg.Clean() {
			t.Errorf("diverged-clean case: %+v", mg)
		}
	})
	t.Run("diverged-conflict", func(t *testing.T) {
		m := landManager(t)
		k := Key{"PROJ-1", "0001"}
		wt := mustCreate(t, m, k.Ticket, k.Attempt)
		writeCommit(t, wt.Path, "clash.txt", "feature side", "feature edit")
		writeCommit(t, m.repo, "clash.txt", "base side", "base edit")
		mg, err := m.Mergeable(context.Background(), k, "main")
		if err != nil {
			t.Fatalf("Mergeable: %v", err)
		}
		if mg.FastForward || mg.Clean() {
			t.Errorf("expected conflicts, got %+v", mg)
		}
		if len(mg.Conflicts) != 1 || mg.Conflicts[0] != "clash.txt" {
			t.Errorf("conflicts = %v, want [clash.txt]", mg.Conflicts)
		}
	})
}

// TestMergeAlreadyUpToDate lands a branch that carries no new commits: an
// idempotent no-op that still reports success.
func TestMergeAlreadyUpToDate(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	mustCreate(t, m, k.Ticket, k.Attempt) // no commits on the branch
	landed, err := m.Merge(context.Background(), k, "main")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !landed.AlreadyUpToDate {
		t.Errorf("expected AlreadyUpToDate, got %+v", landed)
	}
}

// TestMergeBaseDirtyRefuses refuses to land into a base checkout that holds
// uncommitted work a fast-forward would disturb.
func TestMergeBaseDirtyRefuses(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	// Dirty the main repo's checkout (base = main is checked out there).
	if err := os.WriteFile(filepath.Join(m.repo, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := m.Merge(context.Background(), k, "main")
	if !errors.Is(err, ErrBaseDirty) {
		t.Fatalf("Merge err = %v, want ErrBaseDirty", err)
	}
}

// TestMergeMissingBase surfaces a precise sentinel when the recorded base does
// not exist.
func TestMergeMissingBase(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	mustCreate(t, m, k.Ticket, k.Attempt)
	if _, err := m.Merge(context.Background(), k, "nonesuch"); !errors.Is(err, ErrBaseNotFound) {
		t.Fatalf("Merge err = %v, want ErrBaseNotFound", err)
	}
}

// TestSyncMaterializesReclaimedCheckout proves sync works on an attempt whose
// clean checkout the daemon already reclaimed: it re-attaches to the surviving
// branch, merges, and leaves a checkout a resume can continue on.
func TestSyncMaterializesReclaimedCheckout(t *testing.T) {
	m := landManager(t)
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)
	writeCommit(t, wt.Path, "feat.txt", "work", "feature work")
	writeCommit(t, m.repo, "base.txt", "moved", "base moved")
	// Reclaim the checkout (keep the branch), as retire does for a clean terminal.
	if err := m.Remove(context.Background(), k, RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("checkout not reclaimed: %v", err)
	}

	if _, err := m.Sync(context.Background(), k, "main"); err != nil {
		t.Fatalf("Sync after reclaim: %v", err)
	}
	// The checkout is back and now ff-landable.
	if _, err := os.Stat(wt.Path); err != nil {
		t.Errorf("sync did not materialize a checkout: %v", err)
	}
	if mg, _ := m.Mergeable(context.Background(), k, "main"); !mg.FastForward {
		t.Error("branch not ff-landable after sync")
	}
}

func TestHeadBranch(t *testing.T) {
	m := landManager(t)
	branch, ok, err := HeadBranch(context.Background(), m.repo)
	if err != nil || !ok || branch != "main" {
		t.Fatalf("HeadBranch = %q, ok=%t, err=%v; want main", branch, ok, err)
	}
	// Detached HEAD: a real repo, but no branch to default from.
	gitCheckout(t, m.repo, "--detach", "HEAD")
	if _, ok, err := HeadBranch(context.Background(), m.repo); err != nil || ok {
		t.Fatalf("detached HeadBranch ok=%t err=%v; want ok=false", ok, err)
	}
	// A path that is not a git repo is a real error the caller can distinguish.
	if _, _, err := HeadBranch(context.Background(), t.TempDir()); err == nil {
		t.Error("expected an error for a non-repo path")
	}
}

// --- helpers ---

func isReachable(t *testing.T, dir, commit string) bool {
	t.Helper()
	return exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", commit, "HEAD").Run() == nil
}

func revList(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "log"}, args...)...).Output()
	if err != nil {
		t.Fatalf("git log %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}
