package web

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
)

// gitpos_test.go covers drv-022's Git-controls position indicator: the pure fold
// (projectPosition) across every ahead/behind shape, the live reader
// (gatherGitPosition) over real fixture repos for the four comparison cases, its
// best-effort fallbacks (no remote / detached HEAD / no upstream), and the rendered
// panel line driven off a stubbed git layer.

// ---- the pure fold: projectPosition ----

func TestProjectPosition(t *testing.T) {
	cases := []struct {
		name        string
		st          gitPositionState
		wantBranch  string
		wantHasB    bool
		wantHasC    bool
		wantSummary string
	}{
		{"up to date", gitPositionState{Branch: "feature", BranchOK: true, CountOK: true}, "feature", true, true, "up to date"},
		{"ahead only", gitPositionState{Branch: "feature", BranchOK: true, Ahead: 2, CountOK: true}, "feature", true, true, "ahead 2 · behind 0"},
		{"behind only", gitPositionState{Branch: "feature", BranchOK: true, Behind: 3, CountOK: true}, "feature", true, true, "ahead 0 · behind 3"},
		{"diverged", gitPositionState{Branch: "feature", BranchOK: true, Ahead: 2, Behind: 3, CountOK: true}, "feature", true, true, "ahead 2 · behind 3"},
		{"branch known, no count", gitPositionState{Branch: "feature", BranchOK: true}, "feature", true, false, ""},
		{"nothing known", gitPositionState{}, "unknown", false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vm := projectPosition(tc.st)
			if vm == nil {
				t.Fatal("projectPosition returned nil; the line must always render")
			}
			if vm.Branch != tc.wantBranch || vm.HasBranch != tc.wantHasB {
				t.Errorf("branch = %q/%v, want %q/%v", vm.Branch, vm.HasBranch, tc.wantBranch, tc.wantHasB)
			}
			if vm.HasCount != tc.wantHasC {
				t.Errorf("HasCount = %v, want %v", vm.HasCount, tc.wantHasC)
			}
			if vm.Summary != tc.wantSummary {
				t.Errorf("Summary = %q, want %q", vm.Summary, tc.wantSummary)
			}
		})
	}
}

// ---- the live reader: gatherGitPosition over real fixture repos ----

// posRepo builds a real git repo on a `feature` branch with an `origin` remote whose
// `feature` ref mirrors the local tip — the up-to-date baseline each ahead/behind
// case diverges from. XDG_CACHE_HOME is redirected so Locate finds no managed
// worktree and the reader falls back to reading the base repo directly.
func posRepo(t *testing.T) (root store.Root, repo string, a project.Attempt) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	repo = t.TempDir()
	gitInWeb(t, repo, "init", "-q", "-b", "main")
	gitInWeb(t, repo, "config", "user.email", "t@t")
	gitInWeb(t, repo, "config", "user.name", "t")
	gitInWeb(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	bare := t.TempDir()
	gitInWeb(t, bare, "init", "-q", "--bare", "-b", "main")
	gitInWeb(t, repo, "remote", "add", "origin", bare)
	gitInWeb(t, repo, "push", "-q", "origin", "main")
	gitInWeb(t, repo, "checkout", "-q", "-b", "feature")
	gitInWeb(t, repo, "push", "-q", "origin", "feature") // origin/feature == feature (up to date)

	data := t.TempDir()
	root = store.Root{Dir: data}
	seedReviewTicket(t, root, "POS-1", "0001", repo, "main")
	a, err := project.LoadAttempt(root, "POS-1", "0001")
	if err != nil {
		t.Fatalf("LoadAttempt: %v", err)
	}
	return root, repo, a
}

func TestGatherGitPosition(t *testing.T) {
	t.Run("up to date", func(t *testing.T) {
		_, _, a := posRepo(t)
		s := bareServer(t)
		st := s.gatherGitPosition(context.Background(), a)
		assertPos(t, st, "feature", 0, 0, true)
	})

	t.Run("ahead only", func(t *testing.T) {
		_, repo, a := posRepo(t)
		gitInWeb(t, repo, "commit", "-q", "--allow-empty", "-m", "local ahead") // not pushed
		s := bareServer(t)
		st := s.gatherGitPosition(context.Background(), a)
		assertPos(t, st, "feature", 1, 0, true)
	})

	t.Run("behind only", func(t *testing.T) {
		_, repo, a := posRepo(t)
		// Push an extra commit (advances origin/feature + the tracking ref), then drop it
		// locally so the branch trails the remote.
		gitInWeb(t, repo, "commit", "-q", "--allow-empty", "-m", "remote ahead")
		gitInWeb(t, repo, "push", "-q", "origin", "feature")
		gitInWeb(t, repo, "reset", "-q", "--hard", "HEAD~1")
		s := bareServer(t)
		st := s.gatherGitPosition(context.Background(), a)
		assertPos(t, st, "feature", 0, 1, true)
	})

	t.Run("diverged", func(t *testing.T) {
		_, repo, a := posRepo(t)
		// Advance origin/feature by one, drop it locally, then add a different local
		// commit: each side now holds one commit the other lacks.
		gitInWeb(t, repo, "commit", "-q", "--allow-empty", "-m", "remote side")
		gitInWeb(t, repo, "push", "-q", "origin", "feature")
		gitInWeb(t, repo, "reset", "-q", "--hard", "HEAD~1")
		gitInWeb(t, repo, "commit", "-q", "--allow-empty", "-m", "local side")
		s := bareServer(t)
		st := s.gatherGitPosition(context.Background(), a)
		assertPos(t, st, "feature", 1, 1, true)
	})
}

// ---- best-effort fallbacks: the graceful-degradation AC ----

func TestGatherGitPositionFallback(t *testing.T) {
	t.Run("no primary remote", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not on PATH")
		}
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		repo := t.TempDir()
		gitInWeb(t, repo, "init", "-q", "-b", "main")
		gitInWeb(t, repo, "config", "user.email", "t@t")
		gitInWeb(t, repo, "config", "user.name", "t")
		gitInWeb(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
		gitInWeb(t, repo, "checkout", "-q", "-b", "feature")
		// No remote configured at all.
		root := store.Root{Dir: t.TempDir()}
		seedReviewTicket(t, root, "POS-1", "0001", repo, "main")
		a, _ := project.LoadAttempt(root, "POS-1", "0001")
		st := bareServer(t).gatherGitPosition(context.Background(), a)
		// Branch is still read; the count is omitted (no remote to compare against).
		assertPos(t, st, "feature", 0, 0, false)
		if !st.BranchOK {
			t.Error("branch should still be read without a remote")
		}
	})

	t.Run("no upstream ref", func(t *testing.T) {
		// posRepo pushes feature; drop the tracking ref so the branch has no upstream.
		_, repo, a := posRepo(t)
		gitInWeb(t, repo, "update-ref", "-d", "refs/remotes/origin/feature")
		st := bareServer(t).gatherGitPosition(context.Background(), a)
		assertPos(t, st, "feature", 0, 0, false)
		if !st.BranchOK {
			t.Error("branch should still be read with no upstream")
		}
	})

	t.Run("detached HEAD", func(t *testing.T) {
		_, repo, a := posRepo(t)
		gitInWeb(t, repo, "checkout", "-q", "--detach", "HEAD")
		st := bareServer(t).gatherGitPosition(context.Background(), a)
		if st.BranchOK {
			t.Errorf("detached HEAD must not report a branch, got %q", st.Branch)
		}
		if st.CountOK {
			t.Error("detached HEAD must omit the count")
		}
	})

	t.Run("no repo recorded", func(t *testing.T) {
		st := (&Server{}).gatherGitPosition(context.Background(), project.Attempt{})
		if st.BranchOK || st.CountOK {
			t.Errorf("an attempt with no repo must gather nothing, got %+v", st)
		}
	})
}

// ---- the rendered panel line, driven off a stubbed git layer ----

func TestGitPositionRendersOnPanel(t *testing.T) {
	root := seedReviewAttempt(t, "/mock/repo", "POS-2", "0001",
		event.Event{Type: "created", Actor: "a"},
		event.Event{Type: "review", Actor: "a", Body: "ready"},
	)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	s.gitPos = func(context.Context, project.Attempt) gitPositionState {
		return gitPositionState{Branch: "draiver/POS-2/0001", BranchOK: true, Ahead: 2, Behind: 3, CountOK: true}
	}
	body := get(t, s.Handler(), "/ticket/POS-2/0001").Body.String()
	if !strings.Contains(body, `data-testid="git-position"`) {
		t.Fatalf("git panel missing the position line\n%s", body)
	}
	if !strings.Contains(body, "draiver/POS-2/0001") {
		t.Error("position line does not show the current branch")
	}
	if !strings.Contains(body, "ahead 2 · behind 3") {
		t.Error("position line does not show the ahead/behind summary")
	}
}

func TestGitPositionDegradesOnPanel(t *testing.T) {
	root := seedReviewAttempt(t, "/mock/repo", "POS-3", "0001",
		event.Event{Type: "created", Actor: "a"},
		event.Event{Type: "review", Actor: "a", Body: "ready"},
	)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	// A git layer that resolved nothing (detached / no repo): the line still renders,
	// "unknown" with the count omitted.
	s.gitPos = func(context.Context, project.Attempt) gitPositionState { return gitPositionState{} }
	body := get(t, s.Handler(), "/ticket/POS-3/0001").Body.String()
	if !strings.Contains(body, `data-testid="git-position"`) {
		t.Fatalf("git panel missing the position line\n%s", body)
	}
	if !strings.Contains(body, `data-testid="git-branch">unknown<`) {
		t.Error("degraded branch should read 'unknown'")
	}
	if !strings.Contains(body, "position vs remote unknown") {
		t.Error("degraded count should be omitted with an explicit note")
	}
}

// ---- small helpers ----

func assertPos(t *testing.T, st gitPositionState, branch string, ahead, behind int, countOK bool) {
	t.Helper()
	if st.Branch != branch {
		t.Errorf("branch = %q, want %q", st.Branch, branch)
	}
	if st.CountOK != countOK {
		t.Errorf("CountOK = %v, want %v", st.CountOK, countOK)
	}
	if countOK && (st.Ahead != ahead || st.Behind != behind) {
		t.Errorf("ahead/behind = %d/%d, want %d/%d", st.Ahead, st.Behind, ahead, behind)
	}
}

// newServer builds a bare Server carrying the default git reader (the one under
// test): gatherGitPosition only needs the attempt's recorded repo path, so no data
// root binding is required.
func bareServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(store.Root{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
