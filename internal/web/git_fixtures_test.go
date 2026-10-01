package web

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// Shared fixtures for the Git-control web tests (git_controls_test.go): they build
// a real git repo with a per-attempt branch and an `origin` remote, in-process (no
// CLI binary — drv-008), so the push/sync handlers run end-to-end.

// writeAttemptMeta writes a minimal attempt.md so an attempt loads with a recorded
// repo+base — the gate the Git section renders behind. No git is touched; this only
// exercises the template gating.
func writeAttemptMeta(t *testing.T, root store.Root, id, att, repo, base string) {
	t.Helper()
	fm := "---\nid: " + att + "\nticket: " + id + "\nrepo: " + repo + "\nbase: " + base + "\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(id, att), []byte(fm), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitInWeb(t *testing.T, dir string, args ...string) {
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

// seedReviewTicket seeds a Review attempt on disk without the CLI: it writes the
// ticket spec, an attempt.md recording repo+base, and the created→review events —
// the in-process equivalent of `draiver new --repo … && draiver review …`.
func seedReviewTicket(t *testing.T, root store.Root, id, att, repo, base string) {
	t.Helper()
	if err := root.EnsureAttemptDirs(id, att); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.SpecPath(id), []byte("---\nid: "+id+"\ntitle: T\n---\n\n# T\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAttemptMeta(t, root, id, att, repo, base)
	if _, err := ticketlog.Append(root, id, att, event.Event{Type: "created", Actor: "human:test", Body: "start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, id, att, event.Event{Type: "review", Actor: "human:test", Body: "ready"}); err != nil {
		t.Fatal(err)
	}
}

func headOfWeb(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", ref, dir, err)
	}
	return strings.TrimSpace(string(out))
}

func stateOfWeb(t *testing.T, root store.Root) project.State {
	t.Helper()
	a, err := project.LoadAttempt(root, "PROJ-1", "0001")
	if err != nil {
		t.Fatalf("LoadAttempt: %v", err)
	}
	return a.State
}

// remoteReviewAttempt builds a Review attempt targeting a real git repo whose
// `origin` remote mirrors main; with merged=true it also pushes the per-attempt
// branch tip onto origin/main, simulating a PR merged on the forge.
func remoteReviewAttempt(t *testing.T, merged bool) (root store.Root, repo, checkout, branch string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// Keep the daemon's managed checkouts off the real cache.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	repo = t.TempDir()
	gitInWeb(t, repo, "init", "-q", "-b", "main")
	gitInWeb(t, repo, "config", "user.email", "t@t")
	gitInWeb(t, repo, "config", "user.name", "t")
	gitInWeb(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	data := t.TempDir()
	root = store.Root{Dir: data}
	// Seed a Review attempt recording repo+base=main, in-process (no CLI binary).
	seedReviewTicket(t, root, "PROJ-1", "0001", repo, "main")

	// Materialize the branch + worktree the daemon would cut and put a commit on it.
	m, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	wt, err := m.Create(context.Background(), worktree.Spec{Key: worktree.Key{Ticket: "PROJ-1", Attempt: "0001"}})
	if err != nil {
		t.Fatalf("Create worktree: %v", err)
	}
	checkout, branch = wt.Path, wt.Branch
	if err := os.WriteFile(filepath.Join(checkout, "feat.txt"), []byte("feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInWeb(t, checkout, "add", "feat.txt")
	gitInWeb(t, checkout, "commit", "-q", "-m", "feat")

	bare := t.TempDir()
	gitInWeb(t, bare, "init", "-q", "--bare", "-b", "main")
	gitInWeb(t, repo, "remote", "add", "origin", bare)
	gitInWeb(t, repo, "push", "-q", "origin", "main")
	if merged {
		gitInWeb(t, repo, "push", "-q", "origin", branch+":refs/heads/main")
	}
	return root, repo, checkout, branch
}
