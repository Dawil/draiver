package overlay

import (
	"os"
	"path/filepath"
	"testing"
)

// mkGitRepo makes dir look like a git repo to IsGitRepo's cheap probe: a top-level
// `.git` entry. The probe is deliberately filesystem-only (no `git` shell-out), so a
// bare `.git` directory is enough to exercise resolution without standing up a real
// repo.
func mkGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("make fake git repo %s: %v", dir, err)
	}
}

func TestResolve_ExplicitWinsWithoutExistenceCheck(t *testing.T) {
	// An explicit per-codebase overlay wins outright and is NOT existence-checked: a
	// mispointed explicit overlay must fail loudly later (when the worktree is cut), not
	// silently fall through to the folder lookup and mask the misconfiguration.
	explicit := filepath.Join(t.TempDir(), "does-not-exist")
	loc, ok, err := Resolve("/codebases/api", explicit, "/overlays")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("explicit overlay should resolve ok=true even when the path does not exist yet")
	}
	if loc.Repo != explicit || loc.Via != "explicit" {
		t.Fatalf("got %+v, want Repo=%q Via=explicit", loc, explicit)
	}
}

func TestResolve_FolderLookupByBasename(t *testing.T) {
	// No explicit overlay: fall through to <overlay_dir>/<basename>, used IFF it exists
	// and is a git repo. The basename of the codebase path is the key.
	overlayDir := t.TempDir()
	mkGitRepo(t, filepath.Join(overlayDir, "api"))

	loc, ok, err := Resolve("/some/where/api", "", overlayDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("folder lookup should resolve ok=true for an existing git repo at <overlay_dir>/<basename>")
	}
	want := filepath.Join(overlayDir, "api")
	if loc.Repo != want || loc.Via != "overlay_dir" {
		t.Fatalf("got %+v, want Repo=%q Via=overlay_dir", loc, want)
	}
}

func TestResolve_FolderCandidateMissingOrNonGitFallsThrough(t *testing.T) {
	overlayDir := t.TempDir()
	// A candidate directory that exists but is NOT a git repo must cleanly fall through
	// to "no overlay", so an operator can point overlay_dir at a folder that covers only
	// some of their repos.
	if err := os.MkdirAll(filepath.Join(overlayDir, "api"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := Resolve("/x/api", "", overlayDir); err != nil || ok {
		t.Fatalf("non-git candidate: got ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	// A missing candidate is likewise a clean no-overlay.
	if _, ok, err := Resolve("/x/missing", "", overlayDir); err != nil || ok {
		t.Fatalf("missing candidate: got ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestResolve_NoExplicitNoOverlayDir_None(t *testing.T) {
	// Step 4: neither an explicit overlay nor a global overlay_dir — no overlay, no error
	// (the caller keeps `draiver test` a successful no-op).
	if _, ok, err := Resolve("/x/api", "", ""); err != nil || ok {
		t.Fatalf("got ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestIsGitRepo(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	mkGitRepo(t, repo)

	if ok, err := IsGitRepo(repo); err != nil || !ok {
		t.Fatalf("repo with .git: got ok=%v err=%v, want ok=true", ok, err)
	}
	if ok, err := IsGitRepo(filepath.Join(root, "plain")); err != nil || ok {
		t.Fatalf("missing path: got ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	if ok, err := IsGitRepo(""); err != nil || ok {
		t.Fatalf("empty path: got ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestActive_ScriptEnv(t *testing.T) {
	// The overlay execution contract: DRAIVER_CODEBASE_DIR → the codebase worktree (cwd),
	// DRAIVER_OVERLAY_DIR → the overlay worktree, in a deterministic order.
	a := Active{
		Location:     Location{Repo: "/overlays/api", Via: "overlay_dir"},
		WorktreePath: "/cache/overlay-wt",
		Commit:       "abc123",
	}
	got := a.ScriptEnv("/cache/codebase-wt")
	want := []string{
		"DRAIVER_CODEBASE_DIR=/cache/codebase-wt",
		"DRAIVER_OVERLAY_DIR=/cache/overlay-wt",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("env[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
