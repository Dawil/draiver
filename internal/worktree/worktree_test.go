package worktree

import (
	"context"
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

	removed, err := m.Reconcile(ctx, []Key{keepWt.Key})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
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
	removed, err := m.Reconcile(ctx, []Key{wt.Key})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
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
	if _, err := m.Reconcile(ctx, nil); err != nil {
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
