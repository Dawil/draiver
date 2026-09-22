package web

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// These are the render-path tests drvweb-021 #21 asked for: they drive the real
// HTTP render (board card + attempt-detail header) over a real git repo, branch,
// .test-pyramid.yaml and test-result log — so they catch a regression the pure
// projectPyramid unit tests cannot, namely the badge failing to reach the page.
//
// The unit tests in pyramid_test.go exercise the fold in isolation; the existing
// web tests (seedBoard) build attempts with no repo, so pyramidBadge returns nil
// at its `a.Repo == ""` guard and the badge markup is never rendered. That gap is
// exactly why #21's "I see the test-result but not the badge" slipped through: the
// attempt had retired to Review and its worktree was reclaimed, so the live-only
// projection went dark. These tests pin both the live-worktree and the
// reclaimed-worktree (branch-only) render.

const testPyramidYAML = "levels:\n" +
	"  - name: unit\n" +
	"    run: go test ./...\n" +
	"  - name: integration\n" +
	"    run: go test -tags=integration ./...\n"

// runGit runs git in dir with a deterministic identity, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// initRepo makes a throwaway git repo on main with one commit, optionally carrying
// a .test-pyramid.yaml (unit→integration). It isolates the worktree manager's base
// under a temp XDG_CACHE_HOME so a live Create never touches the real cache.
func initRepo(t *testing.T, withPyramid bool) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	if withPyramid {
		if err := os.WriteFile(filepath.Join(dir, ".test-pyramid.yaml"), []byte(testPyramidYAML), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", ".test-pyramid.yaml")
	}
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

// seedReviewAttempt writes a store attempt (spec + attempt.md with repo/base) in
// Review with the given log events, and returns the server handler over it.
func seedReviewAttempt(t *testing.T, repo, ticket, id string, events ...event.Event) store.Root {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs(ticket, id); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.SpecPath(ticket),
		[]byte("---\nid: "+ticket+"\ntitle: Pyramid ticket\n---\n\n# Pyramid ticket\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := attempt.WriteMeta(root, attempt.Meta{
		ID: id, Ticket: ticket, Repo: repo, Base: "main", Actor: "human:t",
	}); err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if _, err := ticketlog.Append(root, ticket, id, e); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The two pages a reviewer sees the badge on. Kept as helpers so each test reads
// as "render board / detail, assert badge".
func board(t *testing.T, root store.Root) string {
	t.Helper()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return get(t, s.Handler(), "/").Body.String()
}

func detail(t *testing.T, root store.Root, ticket, id string) string {
	t.Helper()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return get(t, s.Handler(), "/ticket/"+ticket+"/"+id).Body.String()
}

// A live worktree at the recorded commit renders a green badge on board + detail.
func TestRenderPyramidBadge_LiveWorktreeGreen(t *testing.T) {
	repo := initRepo(t, true)
	ticket, id := "PYR-1", "0001"

	m, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := m.Create(context.Background(), worktree.Spec{Key: worktree.Key{Ticket: ticket, Attempt: id}})
	if err != nil {
		t.Fatal(err)
	}
	head := runGit(t, wt.Path, "rev-parse", "HEAD")

	root := seedReviewAttempt(t, repo, ticket, id,
		event.Event{Type: "created", Actor: "a"},
		event.Event{Type: "test-result", Actor: "agent:x", Rung: "integration", Commit: head},
		event.Event{Type: "review", Actor: "agent:x", Body: "please review"},
	)

	badge := `data-testid="pyramid-` + ticket + "-" + id + `"`
	for _, page := range []struct {
		name, body string
	}{{"board", board(t, root)}, {"detail", detail(t, root, ticket, id)}} {
		if !strings.Contains(page.body, badge) {
			t.Fatalf("%s: pyramid badge %s not rendered", page.name, badge)
		}
		if !strings.Contains(page.body, "pyramid-green") || strings.Contains(page.body, `data-stale="true"`) {
			t.Errorf("%s: expected a green (non-stale) badge", page.name)
		}
		if !strings.Contains(page.body, "rung-check") {
			t.Errorf("%s: expected a reached-rung ✓ check", page.name)
		}
	}
}

// #27: a live worktree at the recorded commit but with uncommitted changes must NOT
// render green — re-running `draiver test` might no longer pass. The badge shows the
// dirty state (data-dirty="true", not green) so the board reflects the real branch
// state at a glance.
func TestRenderPyramidBadge_DirtyWorktreeNotGreen(t *testing.T) {
	repo := initRepo(t, true)
	ticket, id := "PYR-5", "0001"

	m, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := m.Create(context.Background(), worktree.Spec{Key: worktree.Key{Ticket: ticket, Attempt: id}})
	if err != nil {
		t.Fatal(err)
	}
	head := runGit(t, wt.Path, "rev-parse", "HEAD")

	// Uncommitted change in the checkout — exactly what #27 is about (editing while a
	// result at HEAD is already logged).
	if err := os.WriteFile(filepath.Join(wt.Path, "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := seedReviewAttempt(t, repo, ticket, id,
		event.Event{Type: "created", Actor: "a"},
		event.Event{Type: "test-result", Actor: "agent:x", Rung: "integration", Commit: head},
		event.Event{Type: "review", Actor: "agent:x", Body: "please review"},
	)

	badge := `data-testid="pyramid-` + ticket + "-" + id + `"`
	for _, page := range []struct {
		name, body string
	}{{"board", board(t, root)}, {"detail", detail(t, root, ticket, id)}} {
		if !strings.Contains(page.body, badge) {
			t.Fatalf("%s: pyramid badge %s not rendered", page.name, badge)
		}
		if strings.Contains(page.body, "pyramid-green") {
			t.Errorf("%s: a dirty worktree must not render green", page.name)
		}
		if !strings.Contains(page.body, "pyramid-dirty") || !strings.Contains(page.body, `data-dirty="true"`) {
			t.Errorf("%s: expected the dirty state (pyramid-dirty, data-dirty=true)", page.name)
		}
	}
}

// The regression from #21: once the attempt retires to Review its worktree is
// reclaimed, so Locate finds nothing — but the branch survives, and the badge must
// still render (read from the branch ref). This is the case the live-only code
// silently dropped.
func TestRenderPyramidBadge_ReclaimedWorktreeStillRenders(t *testing.T) {
	repo := initRepo(t, true)
	ticket, id := "PYR-2", "0001"
	branch := "draiver/" + ticket + "/" + id

	// Branch exists at the pyramid-carrying tip, but there is NO worktree — exactly
	// the state the daemon leaves an attempt in when it retires into Review.
	runGit(t, repo, "branch", branch, "main")
	head := runGit(t, repo, "rev-parse", "refs/heads/"+branch)

	root := seedReviewAttempt(t, repo, ticket, id,
		event.Event{Type: "created", Actor: "a"},
		event.Event{Type: "test-result", Actor: "agent:x", Rung: "integration", Commit: head},
		event.Event{Type: "review", Actor: "agent:x", Body: "please review"},
	)

	badge := `data-testid="pyramid-` + ticket + "-" + id + `"`
	b := board(t, root)
	if !strings.Contains(b, badge) {
		t.Fatalf("board: reclaimed-worktree attempt lost its pyramid badge (%s)", badge)
	}
	if !strings.Contains(b, "pyramid-green") || strings.Contains(b, `data-stale="true"`) {
		t.Errorf("board: expected green-at-HEAD for a result at the branch tip")
	}
	if d := detail(t, root, ticket, id); !strings.Contains(d, badge) {
		t.Fatalf("detail: reclaimed-worktree attempt lost its pyramid badge (%s)", badge)
	}
}

// A result recorded at a commit that is no longer the branch tip renders the stale
// state (dimmed, data-stale="true") rather than green — read via the branch ref.
func TestRenderPyramidBadge_StaleWhenTipMovedOn(t *testing.T) {
	repo := initRepo(t, true)
	ticket, id := "PYR-3", "0001"
	branch := "draiver/" + ticket + "/" + id

	old := runGit(t, repo, "rev-parse", "HEAD")        // the commit a result will name
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "advance")
	runGit(t, repo, "branch", branch, "main")          // branch tip is now past `old`

	root := seedReviewAttempt(t, repo, ticket, id,
		event.Event{Type: "created", Actor: "a"},
		event.Event{Type: "test-result", Actor: "agent:x", Rung: "integration", Commit: old},
		event.Event{Type: "review", Actor: "agent:x", Body: "please review"},
	)

	badge := `data-testid="pyramid-` + ticket + "-" + id + `"`
	b := board(t, root)
	if !strings.Contains(b, badge) {
		t.Fatalf("board: stale attempt lost its pyramid badge (%s)", badge)
	}
	if !strings.Contains(b, "pyramid-stale") || !strings.Contains(b, `data-stale="true"`) {
		t.Errorf("board: expected a stale badge for a result behind the branch tip")
	}
}

// No .test-pyramid.yaml on the branch → no badge, even with a test-result logged.
func TestRenderPyramidBadge_NoPyramidRendersNothing(t *testing.T) {
	repo := initRepo(t, false) // no .test-pyramid.yaml
	ticket, id := "PYR-4", "0001"
	branch := "draiver/" + ticket + "/" + id
	runGit(t, repo, "branch", branch, "main")
	head := runGit(t, repo, "rev-parse", "refs/heads/"+branch)

	root := seedReviewAttempt(t, repo, ticket, id,
		event.Event{Type: "created", Actor: "a"},
		event.Event{Type: "test-result", Actor: "agent:x", Rung: "unit", Commit: head},
		event.Event{Type: "review", Actor: "agent:x", Body: "please review"},
	)

	badge := `data-testid="pyramid-` + ticket + "-" + id + `"`
	if b := board(t, root); strings.Contains(b, badge) {
		t.Fatalf("board: badge rendered for a repo with no .test-pyramid.yaml")
	}
}
