package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// drvweb-030: the pyramid cache must change how often git runs, never what renders.
// These tests drive ONE long-lived Server (so the cache is warm across renders) over
// a real temp git repo, with the cache's clock FROZEN: every "next render reflects
// the change" assertion therefore proves the cheap fingerprint caught the change, not
// a TTL expiring.

// cachedServer builds a Server whose pyramid cache runs on a frozen clock and whose
// config resolution is isolated (no operator overlay leaks in).
func cachedServer(t *testing.T, root store.Root) (*Server, *fakeClock) {
	t.Helper()
	if os.Getenv("DRAIVER_CONFIG") == "" {
		t.Setenv("DRAIVER_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	clk := &fakeClock{t: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)}
	s.pyCache.now = clk.now
	return s, clk
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func renderBoard(t *testing.T, s *Server) string {
	t.Helper()
	return get(t, s.Handler(), "/").Body.String()
}

// badgeState classifies the one attempt's badge on a rendered board.
func badgeState(t *testing.T, body string) string {
	t.Helper()
	switch {
	case !strings.Contains(body, `data-testid="pyramid-`):
		return "none"
	case strings.Contains(body, `data-dirty="true"`):
		return "dirty"
	case strings.Contains(body, `data-stale="true"`):
		return "stale"
	case strings.Contains(body, "pyramid-green"):
		return "green"
	}
	return "unknown"
}

// commitOnBranch advances a branch by one empty commit without a checkout — the
// shape of an agent committing from a worktree, as seen from the base repo.
func commitOnBranch(t *testing.T, repo, branch string) string {
	t.Helper()
	tip := runGit(t, repo, "rev-parse", "refs/heads/"+branch)
	c := runGit(t, repo, "commit-tree", tip+"^{tree}", "-p", tip, "-m", "more")
	runGit(t, repo, "update-ref", "refs/heads/"+branch, c)
	return c
}

func TestPyramidCache_CommitOnBranchGoesStaleNextRender(t *testing.T) {
	for _, packed := range []bool{false, true} {
		t.Run(fmt.Sprintf("packed=%v", packed), func(t *testing.T) {
			repo := initRepo(t, true)
			ticket, id := "PC-1", "0001"
			branch := "draiver/" + ticket + "/" + id
			runGit(t, repo, "branch", branch, "main")
			if packed {
				// Refs moved into packed-refs: the cheap reader must find them there.
				runGit(t, repo, "pack-refs", "--all")
			}
			head := runGit(t, repo, "rev-parse", "refs/heads/"+branch)
			root := seedReviewAttempt(t, repo, ticket, id,
				event.Event{Type: "created", Actor: "a"},
				event.Event{Type: "test-result", Actor: "agent:x", Rung: "integration", Commit: head},
				event.Event{Type: "review", Actor: "agent:x", Body: "r"},
			)
			s, _ := cachedServer(t, root)

			if got := badgeState(t, renderBoard(t, s)); got != "green" {
				t.Fatalf("cold render: badge %q, want green", got)
			}
			cold := s.pyCache.computes.Load()
			if got := badgeState(t, renderBoard(t, s)); got != "green" {
				t.Fatalf("warm render: badge %q, want green", got)
			}
			if warm := s.pyCache.computes.Load(); warm != cold {
				t.Fatalf("warm render re-ran the gatherer (%d → %d computes)", cold, warm)
			}

			commitOnBranch(t, repo, branch)
			if got := badgeState(t, renderBoard(t, s)); got != "stale" {
				t.Fatalf("after a commit on the branch: badge %q, want stale on the very next render", got)
			}
		})
	}
}

func TestPyramidCache_DirtyLiveWorktreeShowsDirtyNextRender(t *testing.T) {
	repo := initRepo(t, true)
	ticket, id := "PC-2", "0001"
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
	)
	s, clk := cachedServer(t, root)

	if got := badgeState(t, renderBoard(t, s)); got != "green" {
		t.Fatalf("clean worktree: badge %q, want green", got)
	}
	renderBoard(t, s) // warm
	scratch := filepath.Join(wt.Path, "scratch.txt")
	if err := os.WriteFile(scratch, []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Dirtiness is sampled once per render window (pyramidDirtyWindow); the next
	// render is one 3s poll later. That is still inside every other TTL, so the
	// assertion below rests on the live dirtiness sample, not an entry expiring.
	clk.advance(3 * time.Second)
	if got := badgeState(t, renderBoard(t, s)); got != "dirty" {
		t.Fatalf("after dirtying the worktree: badge %q, want dirty on the next render", got)
	}
	if err := os.Remove(scratch); err != nil {
		t.Fatal(err)
	}
	clk.advance(3 * time.Second)
	if got := badgeState(t, renderBoard(t, s)); got != "green" {
		t.Fatalf("after cleaning the worktree: badge %q, want green again", got)
	}
}

func TestPyramidCache_ReclaimedWorktreeFallsBackToBranch(t *testing.T) {
	repo := initRepo(t, true)
	ticket, id := "PC-3", "0001"
	key := worktree.Key{Ticket: ticket, Attempt: id}
	m, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := m.Create(context.Background(), worktree.Spec{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	head := runGit(t, wt.Path, "rev-parse", "HEAD")
	root := seedReviewAttempt(t, repo, ticket, id,
		event.Event{Type: "created", Actor: "a"},
		event.Event{Type: "test-result", Actor: "agent:x", Rung: "integration", Commit: head},
	)
	s, _ := cachedServer(t, root)
	if got := badgeState(t, renderBoard(t, s)); got != "green" {
		t.Fatalf("live worktree: badge %q, want green", got)
	}
	// Commit in the worktree, then reclaim it keeping the branch: the board must read
	// the surviving branch tip (stale vs the logged result), not a cached live state.
	runGit(t, wt.Path, "commit", "-q", "--allow-empty", "-m", "after the result")
	if err := m.Remove(context.Background(), key, worktree.RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := m.BranchSHA(context.Background(), key); !ok {
		t.Fatal("fixture: Remove deleted the branch; the test needs it to survive")
	}
	if got := badgeState(t, renderBoard(t, s)); got != "stale" {
		t.Fatalf("after reclaim: badge %q, want stale (branch tip moved past the result)", got)
	}
	snap, _ := s.pyCache.repoSnapshot(context.Background(), repo)
	if _, live := snap.live[key]; live {
		t.Fatal("repo snapshot still lists the reclaimed worktree")
	}
}

func TestPyramidCache_OverlayRevisionMoveRereadsPyramid(t *testing.T) {
	repo := initRepo(t, false) // no in-repo pyramid → the overlay supplies it
	overlayRepo := t.TempDir()
	runGit(t, overlayRepo, "init", "-q", "-b", "main")
	writeCommit := func(yaml string) {
		if err := os.WriteFile(filepath.Join(overlayRepo, ".test-pyramid.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, overlayRepo, "add", ".test-pyramid.yaml")
		runGit(t, overlayRepo, "commit", "-q", "-m", "pyramid")
	}
	writeCommit("levels:\n  - name: unit\n    run: 'true'\n")

	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg, []byte(`{"repos":{"`+repo+`":{"overlay":"`+overlayRepo+`"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRAIVER_CONFIG", cfg)

	ticket, id := "PC-4", "0001"
	branch := "draiver/" + ticket + "/" + id
	runGit(t, repo, "branch", branch, "main")
	head := runGit(t, repo, "rev-parse", "refs/heads/"+branch)
	root := seedReviewAttempt(t, repo, ticket, id,
		event.Event{Type: "created", Actor: "a"},
		event.Event{Type: "test-result", Actor: "agent:x", Rung: "unit", Commit: head},
	)
	s, _ := cachedServer(t, root)

	body := renderBoard(t, s)
	if badgeState(t, body) != "green" || strings.Contains(body, "acceptance") {
		t.Fatalf("overlay at rev 1: want a green one-rung badge, got %q", badgeState(t, body))
	}
	renderBoard(t, s) // warm
	writeCommit("levels:\n  - name: unit\n    run: 'true'\n  - name: acceptance\n    run: 'true'\n")
	if body := renderBoard(t, s); !strings.Contains(body, "acceptance") {
		t.Fatal("after moving the overlay revision: the overlay pyramid was not re-read (no acceptance rung)")
	}
}

// A push/sync from the UI drops the attempt's entry, so the handler's own response
// (and the next render) is computed from git, not served from the cache.
func TestPyramidCache_GitSyncFromUIReflectsImmediately(t *testing.T) {
	root, repo, checkout, branch := remoteReviewAttempt(t, false)
	t.Setenv("DRAIVER_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	if err := os.WriteFile(filepath.Join(checkout, ".test-pyramid.yaml"), []byte(testPyramidYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", ".test-pyramid.yaml")
	runGit(t, checkout, "commit", "-q", "-m", "pyramid")
	head := runGit(t, checkout, "rev-parse", "HEAD")
	if _, err := ticketlog.Append(root, "PROJ-1", "0001", event.Event{Type: "test-result", Actor: "agent:x", Rung: "unit", Commit: head}); err != nil {
		t.Fatal(err)
	}
	s, _ := cachedServer(t, root)
	if got := badgeState(t, renderBoard(t, s)); got != "green" {
		t.Fatalf("before sync: badge %q, want green", got)
	}
	cold := s.pyCache.computes.Load()

	// Advance local main past the branch so sync back-merges and moves the branch.
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "base moved")
	if rec := post(t, s.Handler(), "/ticket/PROJ-1/0001/git/sync"); rec.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", rec.Code, rec.Body)
	}
	if moved := runGit(t, repo, "rev-parse", "refs/heads/"+branch); moved == head {
		t.Fatal("fixture: sync did not move the branch")
	}
	if got := badgeState(t, renderBoard(t, s)); got != "stale" {
		t.Fatalf("after sync: badge %q, want stale (branch advanced past the result)", got)
	}
	if s.pyCache.computes.Load() == cold {
		t.Fatal("after sync: the render was served from the cache")
	}
}

// mustGit / mustGitStdin are testing.TB twins of runGit, usable from a benchmark.
func mustGit(tb testing.TB, dir string, args ...string) string {
	tb.Helper()
	return mustGitStdin(tb, dir, "", args...)
}

func mustGitStdin(tb testing.TB, dir, stdin string, args ...string) string {
	tb.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		tb.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// invalidate drops the entry even when the fingerprint is unchanged — the explicit
// write-path contract, independent of the cheap checks.
func TestPyramidCache_InvalidateForcesRecompute(t *testing.T) {
	c, calls := countingCache(t, func() (pyramidState, bool, error) { return pyramidState{Head: "x"}, true, nil })
	a := cacheAttempt(t)
	c.get(context.Background(), a)
	c.get(context.Background(), a)
	if calls.Load() != 1 {
		t.Fatalf("warm get recomputed: %d calls", calls.Load())
	}
	c.invalidate(a.Ticket, a.ID)
	c.get(context.Background(), a)
	if calls.Load() != 2 {
		t.Fatalf("get after invalidate: %d calls, want 2", calls.Load())
	}
}

// A failed read is kept only briefly; a benign result lives until its fingerprint
// changes (or the long backstop).
func TestPyramidCache_ErrorsCachedOnlyBriefly(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	c, calls := countingCache(t, func() (pyramidState, bool, error) {
		if fail.Load() {
			return pyramidState{}, false, readErr(errors.New("transient"))
		}
		return pyramidState{Head: "x"}, true, nil
	})
	clk := &fakeClock{t: time.Now()}
	c.now = clk.now
	a := cacheAttempt(t)
	if _, ok := c.get(context.Background(), a); ok {
		t.Fatal("want the failure surfaced as ok=false")
	}
	c.get(context.Background(), a) // within the error TTL: cached
	if calls.Load() != 1 {
		t.Fatalf("error not cached at all: %d calls", calls.Load())
	}
	fail.Store(false)
	clk.advance(pyramidErrTTL + time.Millisecond)
	if _, ok := c.get(context.Background(), a); !ok {
		t.Fatal("a transient failure did not recover after the error TTL")
	}
	clk.advance(pyramidSnapTTL) // well past the error TTL, far inside the entry TTL
	c.get(context.Background(), a)
	if calls.Load() != 2 {
		t.Fatalf("a good result was not kept: %d calls", calls.Load())
	}
	clk.advance(pyramidEntryTTL)
	c.get(context.Background(), a)
	if calls.Load() != 3 {
		t.Fatalf("backstop TTL did not expire the entry: %d calls", calls.Load())
	}
}

// Concurrent renders of one attempt share one gatherer run (singleflight).
func TestPyramidCache_SingleflightUnderConcurrency(t *testing.T) {
	release := make(chan struct{})
	c, calls := countingCache(t, func() (pyramidState, bool, error) {
		<-release
		return pyramidState{Head: "x"}, true, nil
	})
	a := cacheAttempt(t)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if st, ok := c.get(context.Background(), a); !ok || st.Head != "x" {
				t.Errorf("concurrent get: %+v %v", st, ok)
			}
		}()
	}
	// Let every goroutine reach the flight before it completes.
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("%d gatherer runs for 16 concurrent gets, want 1", calls.Load())
	}
}

// Entries for attempts no render asks for any more are evicted.
func TestPyramidCache_EvictsIdleEntries(t *testing.T) {
	c, _ := countingCache(t, func() (pyramidState, bool, error) { return pyramidState{Head: "x"}, true, nil })
	clk := &fakeClock{t: time.Now()}
	c.now = clk.now
	a := cacheAttempt(t)
	c.get(context.Background(), a)
	clk.advance(pyramidIdleEvict + 2*time.Minute)
	c.mu.Lock()
	c.maybeSweep(clk.now())
	n := len(c.entries)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d entries survive an idle sweep, want 0", n)
	}
}

// An attempt with neither a branch nor a worktree is answered without the gatherer.
func TestPyramidCache_NoBranchNoWorktreeSkipsGit(t *testing.T) {
	c, calls := countingCache(t, func() (pyramidState, bool, error) { return pyramidState{}, false, nil })
	repo := initRepo(t, true)
	a := project.Attempt{Repo: repo}
	a.Ticket, a.ID = "PC-NB", "0001"
	if _, ok := c.get(context.Background(), a); ok {
		t.Fatal("want ok=false for an attempt with no branch")
	}
	if calls.Load() != 0 {
		t.Fatalf("gatherer ran %d times for a branchless attempt", calls.Load())
	}
}

// countingCache builds a bare cache over a canned gatherer, counting its calls.
func countingCache(t *testing.T, fn func() (pyramidState, bool, error)) (*pyramidCache, *atomic.Int64) {
	t.Helper()
	t.Setenv("DRAIVER_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	var calls atomic.Int64
	c := newPyramidCache(func(context.Context, project.Attempt) (pyramidState, bool, error) {
		calls.Add(1)
		return fn()
	})
	return c, &calls
}

// cacheAttempt is an attempt whose branch exists in a real repo, so the fingerprint
// resolves and the cache (not the bypass) is exercised.
func cacheAttempt(t *testing.T) project.Attempt {
	t.Helper()
	repo := initRepo(t, true)
	runGit(t, repo, "branch", "draiver/PC-C/0001", "main")
	a := project.Attempt{Repo: repo}
	a.Ticket, a.ID = "PC-C", "0001"
	return a
}

// seedDoneAttempts writes n Done attempts into one root, each with a branch-only
// (worktree reclaimed) checkout at a test-result — the measured live-data shape.
func seedDoneAttempts(tb testing.TB, root store.Root, repo string, n int) {
	tb.Helper()
	head := strings.TrimSpace(mustGit(tb, repo, "rev-parse", "main"))
	var refs strings.Builder
	for i := range n {
		ticket := fmt.Sprintf("BEN-%03d", i)
		fmt.Fprintf(&refs, "create refs/heads/draiver/%s/0001 %s\n", ticket, head)
		if err := root.EnsureAttemptDirs(ticket, "0001"); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(root.SpecPath(ticket), []byte("---\nid: "+ticket+"\ntitle: T\n---\n\n# T\n"), 0o644); err != nil {
			tb.Fatal(err)
		}
		if err := attempt.WriteMeta(root, attempt.Meta{ID: "0001", Ticket: ticket, Repo: repo, Base: "main", Actor: "human:t"}); err != nil {
			tb.Fatal(err)
		}
		for _, e := range []event.Event{
			{Type: "created", Actor: "a"},
			{Type: "test-result", Actor: "agent:x", Rung: "integration", Commit: head},
			{Type: "review", Actor: "agent:x", Body: "r"},
			{Type: "done", Actor: "human:t", Body: "ok"},
		} {
			if _, err := ticketlog.Append(root, ticket, "0001", e); err != nil {
				tb.Fatal(err)
			}
		}
	}
	mustGitStdin(tb, repo, refs.String(), "update-ref", "--stdin")
}

// Acceptance: over ~200 branch-only Done attempts, a WARM board render spends well
// under 100ms in the git-derived helper and runs no gatherer at all.
func TestPyramidCache_WarmBoardIsCheap(t *testing.T) {
	repo := initRepo(t, true)
	root := store.Root{Dir: t.TempDir()}
	seedDoneAttempts(t, root, repo, 200)
	s, _ := cachedServer(t, root)
	var spent atomic.Int64
	cached := s.pyramidGit
	s.pyramidGit = func(ctx context.Context, a project.Attempt) (pyramidState, bool) {
		t0 := time.Now()
		defer func() { spent.Add(int64(time.Since(t0))) }()
		return cached(ctx, a)
	}

	body := renderBoard(t, s) // cold
	if n := strings.Count(body, `data-testid="pyramid-BEN-`); n != 200 {
		t.Fatalf("cold render shows %d pyramid badges, want 200", n)
	}
	coldComputes, coldSpent := s.pyCache.computes.Load(), time.Duration(spent.Swap(0))

	t0 := time.Now()
	renderBoard(t, s) // warm
	warmTotal := time.Since(t0)
	warmSpent := time.Duration(spent.Load())
	if c := s.pyCache.computes.Load(); c != coldComputes {
		t.Fatalf("warm render ran the gatherer %d more times", c-coldComputes)
	}
	t.Logf("git-derived helper: cold %v, warm %v (warm render end-to-end %v)", coldSpent, warmSpent, warmTotal)
	if warmSpent > 100*time.Millisecond {
		t.Errorf("warm render spent %v in pyramidGit, want < 100ms", warmSpent)
	}
}

// BenchmarkBoardRender200 compares a cold board render (fresh Server, empty cache)
// with a warm one over 200 branch-only Done attempts.
func BenchmarkBoardRender200(b *testing.B) {
	repo := b.TempDir()
	mustGit(b, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, ".test-pyramid.yaml"), []byte(testPyramidYAML), 0o644); err != nil {
		b.Fatal(err)
	}
	mustGit(b, repo, "add", ".test-pyramid.yaml")
	mustGit(b, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	b.Setenv("XDG_CACHE_HOME", b.TempDir())
	b.Setenv("DRAIVER_CONFIG", filepath.Join(b.TempDir(), "config.json"))
	root := store.Root{Dir: b.TempDir()}
	seedDoneAttempts(b, root, repo, 200)

	renderOnce := func(s *Server) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			b.Fatalf("board: %d", rec.Code)
		}
	}
	b.Run("cold", func(b *testing.B) {
		for b.Loop() {
			s, err := New(root)
			if err != nil {
				b.Fatal(err)
			}
			renderOnce(s)
		}
	})
	b.Run("warm", func(b *testing.B) {
		s, err := New(root)
		if err != nil {
			b.Fatal(err)
		}
		renderOnce(s)
		for b.Loop() {
			renderOnce(s)
		}
	})
}
