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

// newRepo makes a throwaway git repo with one commit and returns its path. Tests
// that need git are skipped when it is absent.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// The default managed base lives under the user cache dir; point it at a temp
	// dir so tests never write into the real ~/.cache.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	run := func(args ...string) {
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
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func newManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(newRepo(t))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func mustCreate(t *testing.T, m *Manager, ticket, attempt string) Worktree {
	t.Helper()
	wt, err := m.Create(context.Background(), Spec{Key: Key{ticket, attempt}})
	if err != nil {
		t.Fatalf("Create %s/%s: %v", ticket, attempt, err)
	}
	return wt
}

func TestCreateProducesCheckoutAndBranch(t *testing.T) {
	m := newManager(t)
	wt := mustCreate(t, m, "PROJ-1", "0001")

	if wt.Key != (Key{"PROJ-1", "0001"}) {
		t.Errorf("key = %+v", wt.Key)
	}
	if wt.Branch != "draiver/PROJ-1/0001" {
		t.Errorf("branch = %q", wt.Branch)
	}
	if fi, err := os.Stat(wt.Path); err != nil || !fi.IsDir() {
		t.Errorf("checkout %q not a dir: %v", wt.Path, err)
	}
	// The checkout sits under the managed base.
	if !strings.HasPrefix(wt.Path, m.Base()+string(filepath.Separator)) {
		t.Errorf("checkout %q not under base %q", wt.Path, m.Base())
	}
	// It's a real, working worktree: a file written there shows up as untracked.
	if err := os.WriteFile(filepath.Join(wt.Path, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	list, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Key != wt.Key {
		t.Fatalf("List = %+v", list)
	}
}

func TestCreateIsIdempotent(t *testing.T) {
	m := newManager(t)
	first := mustCreate(t, m, "PROJ-1", "0001")
	second := mustCreate(t, m, "PROJ-1", "0001")

	if first.Path != second.Path {
		t.Errorf("path drifted: %q vs %q", first.Path, second.Path)
	}
	list, _ := m.List(context.Background())
	if len(list) != 1 {
		t.Fatalf("expected one worktree, got %+v", list)
	}
}

func TestCreateFromRef(t *testing.T) {
	m := newManager(t)
	// Branch the worktree from the first commit, not the tip, to prove Ref is
	// honored: record the tip, add a second commit, then start the attempt at the
	// original point.
	base := revParse(t, m.repo, "HEAD")
	cmd := exec.Command("git", "-C", m.repo, "commit", "-q", "--allow-empty", "-m", "second")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second commit: %v\n%s", err, out)
	}

	wt, err := m.Create(context.Background(), Spec{Key: Key{"PROJ-1", "0001"}, Ref: base})
	if err != nil {
		t.Fatalf("Create from ref: %v", err)
	}
	if head := revParse(t, wt.Path, "HEAD"); head != base {
		t.Errorf("worktree HEAD = %s, want base %s", head, base)
	}
}

func TestRemoveKeepsBranchByDefault(t *testing.T) {
	m := newManager(t)
	wt := mustCreate(t, m, "PROJ-1", "0001")

	if err := m.Remove(context.Background(), wt.Key, RemoveOptions{Force: true}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Errorf("checkout still present: %v", err)
	}
	if list, _ := m.List(context.Background()); len(list) != 0 {
		t.Fatalf("List not empty after Remove: %+v", list)
	}
	// The branch survives, so the attempt can be resumed.
	ok, err := m.branchExists(context.Background(), wt.Branch)
	if err != nil || !ok {
		t.Errorf("branch missing after default Remove (ok=%v err=%v)", ok, err)
	}
}

func TestRemoveDeleteBranch(t *testing.T) {
	m := newManager(t)
	wt := mustCreate(t, m, "PROJ-1", "0001")

	if err := m.Remove(context.Background(), wt.Key, RemoveOptions{Force: true, DeleteBranch: true}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if ok, _ := m.branchExists(context.Background(), wt.Branch); ok {
		t.Error("branch survived DeleteBranch remove")
	}
}

func TestResumeReattachesToKeptBranch(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	wt := mustCreate(t, m, "PROJ-1", "0001")

	// A commit on the attempt branch, made inside its worktree.
	commitIn(t, wt.Path, "work on the attempt")
	want := revParse(t, wt.Path, "HEAD")

	if err := m.Remove(ctx, wt.Key, RemoveOptions{Force: true}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Recreate: must re-attach to the existing branch (Ref is ignored), so the
	// attempt's commit is still there.
	wt2, err := m.Create(ctx, Spec{Key: wt.Key, Ref: "HEAD"})
	if err != nil {
		t.Fatalf("resume Create: %v", err)
	}
	if got := revParse(t, wt2.Path, "HEAD"); got != want {
		t.Errorf("resumed HEAD = %s, lost work (want %s)", got, want)
	}
}

func TestRemoveToleratesCrashedCheckout(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	wt := mustCreate(t, m, "PROJ-1", "0001")

	// Simulate a crash: the checkout directory vanishes under git.
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx, wt.Key, RemoveOptions{Force: true}); err != nil {
		t.Fatalf("Remove of crashed checkout: %v", err)
	}
	if list, _ := m.List(ctx); len(list) != 0 {
		t.Fatalf("stale entry survived: %+v", list)
	}
}

func TestRemoveMissingIsNoOp(t *testing.T) {
	m := newManager(t)
	if err := m.Remove(context.Background(), Key{"PROJ-9", "0009"}, RemoveOptions{Force: true}); err != nil {
		t.Errorf("Remove of never-created worktree errored: %v", err)
	}
}

func TestReconcileRemovesUnwantedKeepsWanted(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	keepWt := mustCreate(t, m, "PROJ-1", "0001")
	dropWt := mustCreate(t, m, "PROJ-2", "0001")

	removed, stray, err := m.Reconcile(ctx, []Key{keepWt.Key})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(stray) != 0 {
		t.Fatalf("unexpected stray entries: %+v", stray)
	}
	if len(removed) != 1 || removed[0].Key != dropWt.Key {
		t.Fatalf("removed = %+v, want just %v", removed, dropWt.Key)
	}
	list, _ := m.List(ctx)
	if len(list) != 1 || list[0].Key != keepWt.Key {
		t.Fatalf("survivors = %+v, want just %v", list, keepWt.Key)
	}
	if _, err := os.Stat(dropWt.Path); !os.IsNotExist(err) {
		t.Errorf("dropped checkout still present")
	}
}

func TestReconcileSweepsPrunableEvenIfKept(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	wt := mustCreate(t, m, "PROJ-1", "0001")

	// Crash: checkout gone, admin entry lingers as prunable.
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatal(err)
	}
	// Even though the daemon still wants this attempt, the dead entry is swept so
	// a fresh Admit can recreate it from the surviving branch.
	removed, stray, err := m.Reconcile(ctx, []Key{wt.Key})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(stray) != 0 {
		t.Fatalf("unexpected stray entries: %+v", stray)
	}
	if len(removed) != 1 || removed[0].Key != wt.Key {
		t.Fatalf("removed = %+v, want the prunable %v", removed, wt.Key)
	}
	if list, _ := m.List(ctx); len(list) != 0 {
		t.Fatalf("prunable entry survived reconcile: %+v", list)
	}
	// The branch remains, so recreation restores the worktree.
	if _, err := m.Create(ctx, Spec{Key: wt.Key}); err != nil {
		t.Fatalf("recreate after sweep: %v", err)
	}
}

func TestListIgnoresForeignWorktrees(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	mustCreate(t, m, "PROJ-1", "0001")

	// A worktree a developer added by hand, outside the managed base.
	foreign := filepath.Join(t.TempDir(), "hand-made")
	if out, err := exec.Command("git", "-C", m.repo, "worktree", "add", "-b", "feature/x", foreign).CombinedOutput(); err != nil {
		t.Fatalf("add foreign worktree: %v\n%s", err, out)
	}

	list, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Ticket != "PROJ-1" {
		t.Fatalf("List leaked a foreign worktree: %+v", list)
	}
	// And Reconcile must never touch it.
	if _, _, err := m.Reconcile(ctx, nil); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("Reconcile clobbered a foreign worktree: %v", err)
	}
}

func TestCreateRejectsUnsafeKeys(t *testing.T) {
	m := newManager(t)
	bad := []Key{
		{"", "0001"},
		{"PROJ-1", ""},
		{"..", "0001"},
		{"PROJ-1", ".."},
		{"a/b", "0001"},
		{"PROJ-1", "a/b"},
		{"PROJ-1", `a\b`},
	}
	for _, k := range bad {
		if _, err := m.Create(context.Background(), Spec{Key: k}); err == nil {
			t.Errorf("Create accepted unsafe key %+v", k)
		}
	}
	if list, _ := m.List(context.Background()); len(list) != 0 {
		t.Fatalf("unsafe keys created worktrees: %+v", list)
	}
}

func TestBaseDefaultsOutsideRepoAndGit(t *testing.T) {
	repo := newRepo(t) // sets XDG_CACHE_HOME to a temp dir
	m, err := NewManager(repo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	want := filepath.Join("draiver", "worktrees")
	if !strings.Contains(m.Base(), want) {
		t.Errorf("base %q does not contain %q", m.Base(), want)
	}
	// The base must NOT sit under .git — a coding agent's auto-mode classifier
	// refuses to write there (drvctl-010).
	if strings.Contains(m.Base(), string(filepath.Separator)+".git"+string(filepath.Separator)) {
		t.Errorf("base %q is under .git", m.Base())
	}
	// Nor inside the repository's working tree, so checkouts never risk an
	// accidental commit and need no .gitignore.
	repoAbs, err := filepath.Abs(repo)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(m.Base(), repoAbs+string(filepath.Separator)) {
		t.Errorf("base %q is inside the repo working tree %q", m.Base(), repoAbs)
	}
	// It lives under the (temp-redirected) user cache dir.
	if cache, err := os.UserCacheDir(); err == nil {
		if !strings.HasPrefix(m.Base(), filepath.Join(cache, "draiver", "worktrees")+string(filepath.Separator)) {
			t.Errorf("base %q not under %q", m.Base(), filepath.Join(cache, "draiver", "worktrees"))
		}
	}
}

func TestNewManagerRejectsNonRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	if _, err := NewManager(t.TempDir()); err == nil {
		t.Error("NewManager accepted a non-git directory")
	}
}

func TestKeyFromBranch(t *testing.T) {
	cases := map[string]Key{
		"draiver/PROJ-1/0001": {"PROJ-1", "0001"},
		"draiver/a-b-c/0002":  {"a-b-c", "0002"},
		"main":                {},
		"feature/x":           {},
		"draiver/onlyticket":  {},
		"draiver/t/a/extra":   {},
		"draiver//0001":       {},
	}
	for branch, want := range cases {
		if got := keyFromBranch(branch); got != want {
			t.Errorf("keyFromBranch(%q) = %+v, want %+v", branch, got, want)
		}
	}
}

// TestDirtyReportsUncommittedWork covers the guard the reconciler consults before
// reclaiming a worktree on retire (drvctl-014): a fresh checkout is clean, an
// untracked file or an uncommitted modification makes it dirty, and committing the
// change makes it clean again.
func TestDirtyReportsUncommittedWork(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	wt := mustCreate(t, m, "PROJ-1", "0001")

	// A just-created checkout mirrors HEAD — clean.
	if dirty, err := m.Dirty(ctx, wt.Key); err != nil || dirty {
		t.Fatalf("fresh checkout: Dirty=%v err=%v, want clean", dirty, err)
	}

	// An untracked file (the drv-002 scenario: implemented, not committed) is dirty.
	untracked := filepath.Join(wt.Path, "feature.go")
	if err := os.WriteFile(untracked, []byte("package feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirty, err := m.Dirty(ctx, wt.Key); err != nil || !dirty {
		t.Fatalf("untracked file: Dirty=%v err=%v, want dirty", dirty, err)
	}

	// Committing the change (commitIn stages everything, including feature.go)
	// returns the checkout to clean.
	commitIn(t, wt.Path, "commit the work")
	if dirty, err := m.Dirty(ctx, wt.Key); err != nil || dirty {
		t.Fatalf("after commit: Dirty=%v err=%v, want clean", dirty, err)
	}

	// A modification to a tracked file is dirty too.
	if err := os.WriteFile(filepath.Join(wt.Path, "f.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirty, err := m.Dirty(ctx, wt.Key); err != nil || !dirty {
		t.Fatalf("modified tracked file: Dirty=%v err=%v, want dirty", dirty, err)
	}
}

// TestDirtyAbsentCheckoutIsClean: a worktree that was never created (or whose
// checkout vanished under a crash) has nothing in a working tree to lose, so it is
// reported clean rather than erroring.
func TestDirtyAbsentCheckoutIsClean(t *testing.T) {
	m := newManager(t)
	if dirty, err := m.Dirty(context.Background(), Key{"PROJ-1", "0001"}); err != nil || dirty {
		t.Fatalf("absent checkout: Dirty=%v err=%v, want clean, no error", dirty, err)
	}
}

// checkoutIn runs `git checkout` inside a checkout dir, simulating a human (or an
// errant script) drifting a managed worktree off its attempt branch (drvctl-046).
func checkoutIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "checkout"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout %v in %s: %v\n%s", args, dir, err, out)
	}
}

// TestCreateVariantAErrorsWhenBranchHeldElsewhere is the drvctl-046 Variant-A
// regression: the attempt's branch is checked out at a *foreign* path (the human's
// origin repo), so `git worktree add` can never succeed. Create must not blindly
// retry it — it returns an ErrCreate that names the offending worktree so a human
// can act, rather than the founding drvctl-027 forever-silent-retry.
func TestCreateVariantAErrorsWhenBranchHeldElsewhere(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)

	// Free the managed checkout but keep the branch, then check that branch out at a
	// path *outside* the managed base — the origin-repo-on-the-branch scenario.
	if err := m.Remove(ctx, k, RemoveOptions{Force: true}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	foreign := filepath.Join(t.TempDir(), "origin-on-branch")
	if out, err := exec.Command("git", "-C", m.repo, "worktree", "add", foreign, wt.Branch).CombinedOutput(); err != nil {
		t.Fatalf("add foreign worktree on branch: %v\n%s", err, out)
	}

	_, err := m.Create(ctx, Spec{Key: k})
	if err == nil {
		t.Fatal("Create succeeded, want an ErrCreate: the branch is checked out elsewhere")
	}
	if !errors.Is(err, ErrCreate) {
		t.Errorf("error is not ErrCreate: %v", err)
	}
	if !strings.Contains(err.Error(), foreign) {
		t.Errorf("error must name the offending worktree %q; got %v", foreign, err)
	}
	// It must not have created (or clobbered) anything: the foreign checkout stands.
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("foreign checkout disturbed: %v", err)
	}
}

// TestCreateVariantBReclaimsDriftedPath is the drvctl-046 Variant-B regression: the
// managed path drifted onto another branch (someone ran `git checkout` inside it).
// find keys on the branch and misses it, so the old code collided on the occupied
// path forever. Create must reclaim the path and succeed on the correct branch.
func TestCreateVariantBReclaimsDriftedPath(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)

	// Drift: check out a different branch inside the managed checkout.
	checkoutIn(t, wt.Path, "-b", "drifted")
	if at, ok, err := m.branchAt(ctx, wt.Path); err != nil || !ok || at != "drifted" {
		t.Fatalf("precondition: branchAt=%q ok=%v err=%v, want drifted", at, ok, err)
	}

	got, err := m.Create(ctx, Spec{Key: k})
	if err != nil {
		t.Fatalf("Create must reclaim a drifted path, got: %v", err)
	}
	if got.Key != k || got.Branch != wt.Branch {
		t.Fatalf("reclaimed worktree = %+v, want key %v on branch %q", got, k, wt.Branch)
	}
	// The checkout is back on the attempt branch, and there is exactly one managed
	// worktree for the attempt.
	if at, _, _ := m.branchAt(ctx, got.Path); at != wt.Branch {
		t.Errorf("after reclaim the path is on %q, want %q", at, wt.Branch)
	}
	if list, _ := m.List(ctx); len(list) != 1 || list[0].Key != k {
		t.Fatalf("List = %+v, want just %v", list, k)
	}
}

// TestCreateVariantBReclaimsDetachedPath is Variant B's detached-HEAD sibling: a
// managed checkout that went detached also yields the zero Key and collides on the
// occupied path. Create must reclaim it just the same.
func TestCreateVariantBReclaimsDetachedPath(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)

	checkoutIn(t, wt.Path, "--detach", "HEAD")
	if _, ok, _ := m.branchAt(ctx, wt.Path); !ok {
		t.Fatal("precondition: expected a detached entry at the path")
	}

	got, err := m.Create(ctx, Spec{Key: k})
	if err != nil {
		t.Fatalf("Create must reclaim a detached path, got: %v", err)
	}
	if got.Branch != wt.Branch {
		t.Fatalf("reclaimed onto %q, want the attempt branch %q", got.Branch, wt.Branch)
	}
}

// TestReconcileToleratesAndReturnsStray is the drvctl-046 bug-2 mechanics: a stray
// (drifted, zero-key) entry under the base must never abort the whole reconcile via
// Remove(Key{}). It is dropped by path and returned — attributed to the attempt its
// path encodes — while every other attempt reconciles normally.
func TestReconcileToleratesAndReturnsStray(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	keepWt := mustCreate(t, m, "PROJ-1", "0001")
	driftWt := mustCreate(t, m, "PROJ-2", "0001")

	// PROJ-2's checkout drifts onto a foreign branch: List now reports it zero-key.
	checkoutIn(t, driftWt.Path, "-b", "hand-checkout")

	removed, stray, err := m.Reconcile(ctx, []Key{keepWt.Key})
	if err != nil {
		t.Fatalf("a stray entry must not abort Reconcile: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed = %+v, want none (PROJ-1 kept, PROJ-2 is a stray)", removed)
	}
	if len(stray) != 1 || stray[0].Key != driftWt.Key {
		t.Fatalf("stray = %+v, want one attributed to %v", stray, driftWt.Key)
	}
	if stray[0].Path != driftWt.Path {
		t.Errorf("stray path = %q, want %q", stray[0].Path, driftWt.Path)
	}
	// The kept attempt survived; the stray was swept from the base.
	list, _ := m.List(ctx)
	if len(list) != 1 || list[0].Key != keepWt.Key {
		t.Fatalf("survivors = %+v, want just %v", list, keepWt.Key)
	}
}

// TestDirtyReportsOnAttemptBranchNotDrift is the drvctl-046 bug-3 regression: Dirty
// must judge k.branch()'s checkout, not whatever branch the path drifted to. A path
// that drifted to a *clean* foreign branch must not be reported clean — that is the
// bug that let the retire guard force-remove a checkout whose real state it never
// read. Drift is reported dirty so the checkout is preserved.
func TestDirtyReportsOnAttemptBranchNotDrift(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	k := Key{"PROJ-1", "0001"}
	wt := mustCreate(t, m, k.Ticket, k.Attempt)

	// Drift onto a brand-new, clean branch. `git status` there is clean, but it is
	// not k.branch(), so Dirty must not trust it.
	checkoutIn(t, wt.Path, "-b", "drifted-clean")
	if out, err := m.gitIn(ctx, wt.Path, "status", "--porcelain"); err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("precondition: drifted branch must be clean; status=%q err=%v", out, err)
	}

	dirty, err := m.Dirty(ctx, k)
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	if !dirty {
		t.Fatal("Dirty reported a drifted checkout clean — the bug-3 misread; want dirty (preserve)")
	}
}

// --- helpers ---

func commitIn(t *testing.T, dir, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", msg}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func revParse(t *testing.T, dir, rev string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", rev).Output()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", rev, dir, err)
	}
	return strings.TrimSpace(string(out))
}
