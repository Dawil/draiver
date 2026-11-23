package brief

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// worstCase is a branch state that overflows every cap at once.
func worstCase() branchState {
	st := branchState{
		Branch: "draiver/PROJ-1/0001", Head: "abc1234",
		Base: "main", BaseTip: "def5678", MergeBase: "0123456",
		BaseAhead: 40,
	}
	for i := 0; i < 50; i++ {
		st.Diff = append(st.Diff, diffFile{Path: fmt.Sprintf("pkg/file%02d.go", i), Add: i, Del: 1})
		st.Status = append(st.Status, fmt.Sprintf(" M dirty%02d.go", i))
	}
	for i := 0; i < maxBaseCommits; i++ {
		st.BaseCommits = append(st.BaseCommits, fmt.Sprintf("c%06d subject %d", i, i))
	}
	return st
}

func lineCount(s string) int { return strings.Count(strings.TrimRight(s, "\n"), "\n") + 1 }

// TestRenderBranchStateWorstCaseIsBounded (drv-064): however much the branch holds,
// the block stays within maxBranchStateLines.
func TestRenderBranchStateWorstCaseIsBounded(t *testing.T) {
	out := renderBranchState(worstCase())
	if n := lineCount(out); n > maxBranchStateLines {
		t.Fatalf("block is %d lines, cap is %d:\n%s", n, maxBranchStateLines, out)
	}
}

// TestRenderBranchStateCaps: each list is cut at its cap with a "+N more" line, and
// the totals still describe the whole diff.
func TestRenderBranchStateCaps(t *testing.T) {
	out := renderBranchState(worstCase())
	for _, want := range []string{
		"Branch `draiver/PROJ-1/0001` @ `abc1234` · base `main` @ `def5678` · merge-base `0123456`",
		"`git diff --stat 0123456...HEAD`): 50 files, +1225 −50",
		fmt.Sprintf("- `pkg/file%02d.go`", maxDiffFiles-1),
		fmt.Sprintf("- …and %d more\n", 50-maxDiffFiles),
		"Uncommitted changes (`git status --short`): 50 paths",
		fmt.Sprintf("- …and %d more\n", 50-maxStatusLines),
		"Base `main` has 40 commits since your merge-base",
		"- c000004 subject 4",
		fmt.Sprintf("- …and %d more\n", 40-maxBaseCommits),
		"`git diff main...HEAD`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("block missing %q:\n%s", want, out)
		}
	}
	for _, absent := range []string{
		fmt.Sprintf("pkg/file%02d.go", maxDiffFiles),
		fmt.Sprintf("dirty%02d.go", maxStatusLines),
	} {
		if strings.Contains(out, absent) {
			t.Errorf("block shows %q past its cap:\n%s", absent, out)
		}
	}
}

// TestRenderBranchStateUnderCaps: short lists render in full, with no "more" lines.
func TestRenderBranchStateUnderCaps(t *testing.T) {
	out := renderBranchState(branchState{
		Branch: "b", Head: "h", Base: "main", BaseTip: "t", MergeBase: "m",
		Diff: []diffFile{{Path: "a.go", Add: 2, Del: 1}, {Path: "img.png", Binary: true}},
	})
	for _, want := range []string{
		"2 files, +2 −1", "- `a.go` +2 −1", "- `img.png` (binary)",
		"none — the tree is clean", "Base `main` has no new commits",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "more") {
		t.Errorf("unexpected cap line:\n%s", out)
	}
}

// TestRenderBranchStateNoBase: an attempt with no base still shows HEAD and the tree.
func TestRenderBranchStateNoBase(t *testing.T) {
	out := renderBranchState(branchState{Head: "h", Status: []string{"?? new.txt"}})
	for _, want := range []string{"detached HEAD", "no base branch recorded", "- `?? new.txt`"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "since your merge-base") {
		t.Errorf("no merge-base, yet the block measures against one:\n%s", out)
	}
}

func TestParseNumstat(t *testing.T) {
	got := parseNumstat("3\t1\ta.go\n-\t-\tlogo.png\n0\t5\tdir/{old => new}.go\n")
	want := []diffFile{{Path: "a.go", Add: 3, Del: 1}, {Path: "logo.png", Binary: true}, {Path: "dir/{old => new}.go", Del: 5}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// TestBuildOmitsBranchStateWithoutWorktree (drv-064): no repo, or a repo with no
// worktree for the attempt, leaves the brief without the block — and without error.
func TestBuildOmitsBranchStateWithoutWorktree(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	repo := gitRepo(t)
	for name, repoPath := range map[string]string{"no repo": "", "missing repo": "/no/such/repo", "no worktree": repo} {
		t.Run(name, func(t *testing.T) {
			root := store.Root{Dir: t.TempDir()}
			seedForLedger(t, root, "PROJ-1", "Wire auth", "", repoPath)
			out, err := Build(root, "PROJ-1", "0001")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out, "## Branch state") {
				t.Fatalf("brief shows a branch-state block with no worktree:\n%s", out)
			}
		})
	}
}

// TestBuildShowsBranchStateAfterBaseMoved (drv-064): end-to-end through a real
// managed worktree — the branch's commit, a dirty file, and the base's new commit
// all surface, measured from the merge-base.
func TestBuildShowsBranchStateAfterBaseMoved(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	repo := gitRepo(t)
	wm, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := wm.Create(context.Background(), worktree.Spec{Key: worktree.Key{Ticket: "PROJ-1", Attempt: "0001"}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, wt.Path, "feat.go", "package feat\n\nfunc F() {}\n")
	git(t, wt.Path, "add", "feat.go")
	git(t, wt.Path, "commit", "-q", "-m", "add feat")
	writeFile(t, wt.Path, "scratch.txt", "wip")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "other ticket landed")

	root := store.Root{Dir: t.TempDir()}
	seedForLedger(t, root, "PROJ-1", "Wire auth", "", repo)
	out, err := Build(root, "PROJ-1", "0001")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Branch state",
		"Branch `draiver/PROJ-1/0001`",
		"base `main`",
		"1 file, +3 −0",
		"- `feat.go` +3 −0",
		"- `?? scratch.txt`",
		"Base `main` has 1 commit since your merge-base",
		"other ticket landed",
		"`git diff main...HEAD`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("brief missing %q:\n%s", want, out)
		}
	}
	if i, j := strings.Index(out, "## Branch state"), strings.Index(out, "## Current understanding"); i > j {
		t.Errorf("branch state should precede the fact index")
	}
}

// gitRepo makes a repo on `main` with one commit.
func gitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "config", "user.email", "t@example.com")
	git(t, repo, "config", "user.name", "t")
	writeFile(t, repo, "README.md", "hi\n")
	git(t, repo, "add", "README.md")
	git(t, repo, "commit", "-q", "-m", "init")
	return repo
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

