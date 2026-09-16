//go:build integration

package integration

import (
	"strings"
	"testing"
)

// TestForgeMergeRemoteContainment exercises the loosely-coupled-forge coupling
// for real: `draiver ctl merge --remote` fetches the base from a live Forgejo/
// Gitea container and lands (records `done`) only when the attempt branch is
// contained upstream — the exact gate the unit tests can only fake with a bare
// local repo. draiver never pushes and never touches the forge API; the test
// itself pushes the "merged PR" state onto the forge, then asserts what merge
// --remote does with the containment check.
//
// The forge container is started once and shared by the subtests (startup is the
// expensive part); each subtest gets its own repo, data root, and forge repo so
// they stay independent.
func TestForgeMergeRemoteContainment(t *testing.T) {
	forge := NewForge(t) // skips cleanly if no container runtime

	t.Run("contained upstream records done", func(t *testing.T) {
		repo := NewGitRepo(t, SeedSingleCommit)
		d := NewDraiver(t)

		// Record a Review attempt whose branch carries a commit on top of main.
		d.Run("new", "PROJ-1", "--title", "Feature", "--repo", repo.Dir, "--base", "main")
		repo.Git("checkout", "-q", "-b", "draiver/PROJ-1/0001")
		repo.Commit("feat.txt", "the work\n", "feat: the work")
		repo.Git("checkout", "-q", "main")
		d.Run("review", "PROJ-1", "ready for review")

		// Push to the forge and simulate the PR being merged: fast-forward the
		// forge's main up to the attempt branch's tip. Now the branch is contained
		// in refs/remotes/forge/main once merge --remote fetches it.
		url := forge.CreateRepo("under-test-1")
		repo.Git("remote", "add", "forge", url)
		repo.Git("push", "-q", "forge", "main")
		repo.Git("push", "-q", "forge", "draiver/PROJ-1/0001:refs/heads/main")

		out, code := d.RunAllowFail("ctl", "merge", "--remote=forge", "PROJ-1@0001")
		if code != 0 {
			t.Fatalf("merge --remote on a contained branch should succeed, got exit %d\n%s", code, out)
		}
		if !d.HasEvent("PROJ-1", "0001", "done") {
			t.Fatalf("contained branch should record a `done` event; log has none\noutput:\n%s", out)
		}
	})

	t.Run("not contained refuses and records nothing", func(t *testing.T) {
		repo := NewGitRepo(t, SeedSingleCommit)
		d := NewDraiver(t)
		// A human actor with --no-escalate makes the refusal a plain nonzero exit
		// with no durable event — the cleanest shape to assert "records nothing".
		d.SetActor("human:test")

		d.Run("new", "PROJ-2", "--title", "Unmerged", "--repo", repo.Dir, "--base", "main")
		repo.Git("checkout", "-q", "-b", "draiver/PROJ-2/0001")
		repo.Commit("wip.txt", "not merged upstream\n", "wip")
		repo.Git("checkout", "-q", "main")
		d.Run("review", "PROJ-2", "ready for review")

		// Push only main to the forge — the branch is NOT merged, so forge/main
		// does not contain it.
		url := forge.CreateRepo("under-test-2")
		repo.Git("remote", "add", "forge", url)
		repo.Git("push", "-q", "forge", "main")

		out, code := d.RunAllowFail("ctl", "merge", "--remote=forge", "--no-escalate", "PROJ-2@0001")
		if code == 0 {
			t.Fatalf("merge --remote on an uncontained branch must refuse (nonzero exit); got success\n%s", out)
		}
		if d.HasEvent("PROJ-2", "0001", "done") {
			t.Fatalf("an uncontained branch must record nothing, but a `done` event was written\noutput:\n%s", out)
		}
	})

	t.Run("not contained escalates for an agent actor", func(t *testing.T) {
		repo := NewGitRepo(t, SeedSingleCommit)
		d := NewDraiver(t) // default actor agent:claude-code

		d.Run("new", "PROJ-3", "--title", "Unmerged agent", "--repo", repo.Dir, "--base", "main")
		repo.Git("checkout", "-q", "-b", "draiver/PROJ-3/0001")
		repo.Commit("wip.txt", "still not merged\n", "wip")
		repo.Git("checkout", "-q", "main")
		d.Run("review", "PROJ-3", "ready for review")

		url := forge.CreateRepo("under-test-3")
		repo.Git("remote", "add", "forge", url)
		repo.Git("push", "-q", "forge", "main")

		// Agent default disposition raises a durable escalation and halts (exit 3).
		out, code := d.RunAllowFail("ctl", "merge", "--remote=forge", "PROJ-3@0001")
		if code != 3 {
			t.Fatalf("an agent's failed containment should escalate (exit 3); got exit %d\n%s", code, out)
		}
		if d.HasEvent("PROJ-3", "0001", "done") {
			t.Fatalf("a refused land must not record `done`\n%s", out)
		}
		if !d.HasEvent("PROJ-3", "0001", "escalation") {
			t.Fatalf("an agent's failed containment should record an escalation event\n%s", out)
		}
		if !strings.Contains(out, "escalated") {
			t.Fatalf("expected the escalation to be reported on stdout, got:\n%s", out)
		}
	})
}
