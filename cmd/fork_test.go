package cmd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// forkFixture builds a real git repo, a PROJ-1 ticket bound to it, the attempt's
// managed worktree (under a temp XDG cache, never the real one), and a
// session.json naming that worktree. It returns the data dir, the repo and the
// worktree path.
func forkFixture(t *testing.T, id session.Identity) (dir, repo, wtPath string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	repo = t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	dir = t.TempDir()
	if out, code := run(t, "--data", dir, "--actor", "human:test", "new", "PROJ-1", "--title", "Fork", "--repo", repo); code != 0 {
		t.Fatalf("new exited %d: %s", code, out)
	}
	wm, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := wm.Create(context.Background(), worktree.Spec{Key: worktree.Key{Ticket: "PROJ-1", Attempt: "0001"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		wm.Remove(context.Background(), worktree.Key{Ticket: "PROJ-1", Attempt: "0001"}, worktree.RemoveOptions{Force: true})
	})
	id.Worktree = wt.Path
	writeForkSession(t, dir, id)
	return dir, repo, wt.Path
}

func writeForkSession(t *testing.T, dir string, id session.Identity) {
	t.Helper()
	root := store.Root{Dir: dir}
	if err := root.EnsureSessionDir("PROJ-1", "0001"); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(id)
	if err := os.WriteFile(root.SessionMetaPath("PROJ-1", "0001"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func forkPrintOut(t *testing.T, dir string) (string, int, error) {
	t.Helper()
	return runE(t, "--data", dir, "fork", "PROJ-1@0001", "--print")
}

func TestForkPrintsCwdAndArgv(t *testing.T) {
	dir, _, wt := forkFixture(t, session.Identity{Adapter: "claude-code", SessionID: "sess-1"})
	out, code, err := forkPrintOut(t, dir)
	if code != 0 {
		t.Fatalf("fork exited %d: %v\n%s", code, err, out)
	}
	if !strings.Contains(out, "cwd: "+wt+"\n") {
		t.Errorf("cwd not the worktree %s:\n%s", wt, out)
	}
	if !strings.Contains(out, "argv: claude --resume sess-1 --fork-session --permission-mode plan") {
		t.Errorf("unexpected argv:\n%s", out)
	}
	if strings.Contains(out, "recreated") {
		t.Errorf("present worktree reported as recreated:\n%s", out)
	}
}

func TestForkRecreatesReclaimedWorktree(t *testing.T) {
	dir, repo, wt := forkFixture(t, session.Identity{Adapter: "claude-code", SessionID: "sess-1"})
	// Reclaim it exactly as the daemon does on retire: remove the checkout, keep the branch.
	wm, _ := worktree.NewManager(repo)
	if err := wm.Remove(context.Background(), worktree.Key{Ticket: "PROJ-1", Attempt: "0001"}, worktree.RemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree still present after remove: %v", err)
	}

	out, code, err := forkPrintOut(t, dir)
	if code != 0 {
		t.Fatalf("fork exited %d: %v\n%s", code, err, out)
	}
	if !strings.Contains(out, "recreated "+wt) || !strings.Contains(out, "cwd: "+wt+"\n") {
		t.Errorf("expected a recreate note and cwd %s:\n%s", wt, out)
	}
	head, err := exec.Command("git", "-C", wt, "branch", "--show-current").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(head)); got != "draiver/PROJ-1/0001" {
		t.Errorf("recreated worktree on branch %q, want draiver/PROJ-1/0001", got)
	}
}

func TestForkRefusesWhenBranchGone(t *testing.T) {
	dir, repo, wt := forkFixture(t, session.Identity{Adapter: "claude-code", SessionID: "sess-1"})
	wm, _ := worktree.NewManager(repo)
	if err := wm.Remove(context.Background(), worktree.Key{Ticket: "PROJ-1", Attempt: "0001"}, worktree.RemoveOptions{Force: true, DeleteBranch: true}); err != nil {
		t.Fatal(err)
	}
	out, code, err := forkPrintOut(t, dir)
	if code == 0 {
		t.Fatalf("fork succeeded with the branch gone:\n%s", out)
	}
	if err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Errorf("error = %v, want a branch-gone refusal", err)
	}
	if _, serr := os.Stat(wt); !os.IsNotExist(serr) {
		t.Errorf("worktree recreated on a fresh branch despite the attempt branch being gone")
	}
}

func TestForkRefusesUnforkableSessions(t *testing.T) {
	cases := map[string]session.Identity{
		"empty session id": {Adapter: "claude-code", SessionID: ""},
		"other adapter":    {Adapter: "aider", SessionID: "s"},
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			dir, _, _ := forkFixture(t, id)
			if out, code, _ := forkPrintOut(t, dir); code == 0 {
				t.Fatalf("fork succeeded for %s:\n%s", name, out)
			}
		})
	}
}

func TestForkRefusesWithoutSession(t *testing.T) {
	dir := newTicket(t)
	_, code, err := runE(t, "--data", dir, "fork", "PROJ-1", "--print")
	if code == 0 || err == nil || !strings.Contains(err.Error(), "no session") {
		t.Fatalf("code=%d err=%v; want a no-session refusal", code, err)
	}
}
