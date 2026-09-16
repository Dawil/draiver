//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GitRepo is a local git working tree under test — the checkout an attempt
// targets, seeded to a known commit graph so merge/sync cases are deterministic.
// It is a plain on-disk repo (the same thing the ctl_land unit tests build with
// `git init`), not a container: the forge is what runs in a container.
type GitRepo struct {
	Dir string
	t   *testing.T
}

// GitSeed shapes a fresh repo's initial commit graph.
type GitSeed func(r *GitRepo)

// NewGitRepo initialises a repo on branch main with a deterministic identity and
// applies the seed. XDG_CACHE_HOME is not touched here; callers that cut managed
// worktrees should isolate it themselves.
func NewGitRepo(t *testing.T, seed GitSeed) *GitRepo {
	t.Helper()
	dir := t.TempDir()
	r := &GitRepo{Dir: dir, t: t}
	r.Git("init", "-q", "-b", "main")
	r.Git("config", "user.email", "harness@draiver.test")
	r.Git("config", "user.name", "Integration Harness")
	// Keep commits reproducible and prompt-free.
	if seed != nil {
		seed(r)
	}
	return r
}

// SeedSingleCommit gives the repo one commit on main — enough for board scenarios
// and as the base for a merge round-trip.
func SeedSingleCommit(r *GitRepo) {
	r.Write("README.md", "# under test\n")
	r.Git("add", "README.md")
	r.Git("commit", "-q", "-m", "init")
}

// Git runs `git -C <dir> <args...>` with a fixed author/committer identity and no
// terminal prompts, failing the test on error.
func (r *GitRepo) Git(args ...string) string {
	r.t.Helper()
	out, err := r.git(args...)
	if err != nil {
		r.t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (r *GitRepo) git(args ...string) (string, error) {
	full := append([]string{"-C", r.Dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Integration Harness", "GIT_AUTHOR_EMAIL=harness@draiver.test",
		"GIT_COMMITTER_NAME=Integration Harness", "GIT_COMMITTER_EMAIL=harness@draiver.test",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Write creates or overwrites a file (creating parent dirs) in the working tree.
func (r *GitRepo) Write(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// Commit writes a file and commits it on the current branch, returning the new
// HEAD sha.
func (r *GitRepo) Commit(rel, content, msg string) string {
	r.t.Helper()
	r.Write(rel, content)
	r.Git("add", rel)
	r.Git("commit", "-q", "-m", msg)
	return r.Head()
}

// Head returns the current HEAD commit sha.
func (r *GitRepo) Head() string {
	r.t.Helper()
	return strings.TrimSpace(r.Git("rev-parse", "HEAD"))
}

// RevParse resolves any revision to a sha, returning ok=false when it does not
// exist (e.g. a not-yet-fetched remote ref).
func (r *GitRepo) RevParse(ref string) (string, bool) {
	r.t.Helper()
	out, err := r.git("rev-parse", "--verify", "-q", ref)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}
