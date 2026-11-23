package acceptance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/worktree"
)

// steps_gitcache_drvweb030.go realises drvweb-030's acceptance criteria
// (features/board-cache/git-state-cache.feature): the board's per-attempt git state is
// cached in memory, and the cache never shows a stale badge. Every scenario keeps ONE
// internal/web server alive across renders, so the cache is warm between them exactly
// as in a running webui.

const (
	gc030Ticket = "GC-1"
	gc030Att    = "0001"
	gc030YAML   = "levels:\n  - name: unit\n    run: 'true'\n  - name: acceptance\n    run: 'true'\n"
)

func init() {
	steps := map[string]StepFunc{
		"a board of 200 Done attempts whose worktrees were reclaimed but whose branches survive": givenGC030ManyDone,
		"the board is rendered cold and then warm":                                               whenGC030ColdWarm,
		"the warm render shows every pyramid badge exactly as the cold render did":               thenGC030WarmSame,
		"the warm render takes well under half a second":                                         thenGC030WarmFast,
		"a board with a Review attempt green at its branch tip":                                  givenGC030Branch,
		"a board with a Review attempt green in its live worktree":                               givenGC030Live,
		"the board has been rendered so its git state is cached":                                 givenGC030Warm,
		"a new commit lands on the attempt's branch":                                             whenGC030Commit,
		"the next board render shows the attempt's badge as stale":                               thenGC030Badge("stale", 0),
		"an uncommitted file appears in the attempt's worktree":                                  whenGC030Dirty,
		"the next poll's board render shows the attempt's badge as dirty":                        thenGC030Badge("dirty", 1100*time.Millisecond),
		"the attempt commits again and its worktree is reclaimed, keeping the branch":            whenGC030Reclaim,
		"the next board render reads the branch tip and shows the attempt's badge as stale":      thenGC030Badge("stale", 0),
	}
	for text, fn := range steps {
		if _, clash := registry[text]; clash {
			panic("duplicate acceptance step registration: " + text)
		}
		registry[text] = fn
	}
}

// gc030 is one scenario's fixture: the repo, the live server, and the renders taken.
type gc030 struct {
	repo     string
	checkout string // live worktree path ("" for branch-only attempts)
	board    *board
	cold     string
	warm     string
	warmTook time.Duration
}

func currentGC030(w *World) (*gc030, error) {
	f, _ := w.get("gc030").(*gc030)
	if f == nil {
		return nil, errors.New("no drvweb-030 board set up in scope")
	}
	return f, nil
}

// seedGC030Repo makes a temp repo whose main carries the pyramid, with the managed
// worktree base and the operator config redirected to scenario-scoped temp paths.
func seedGC030Repo(w *World) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("git not on PATH")
	}
	for _, kv := range [][2]string{{"XDG_CACHE_HOME", ""}, {"DRAIVER_CONFIG", "config.json"}} {
		dir, err := os.MkdirTemp("", "gc030-env-")
		if err != nil {
			return "", err
		}
		prev, had := os.LookupEnv(kv[0])
		os.Setenv(kv[0], filepath.Join(dir, kv[1]))
		w.defer_(func() {
			if had {
				os.Setenv(kv[0], prev)
			} else {
				os.Unsetenv(kv[0])
			}
			os.RemoveAll(dir)
		})
	}
	repo, err := os.MkdirTemp("", "gc030-repo-")
	if err != nil {
		return "", err
	}
	w.defer_(func() { os.RemoveAll(repo) })
	if err := gitc(repo, "init", "-q", "-b", "main"); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(repo, ".test-pyramid.yaml"), []byte(gc030YAML), 0o644); err != nil {
		return "", err
	}
	for _, args := range [][]string{{"add", ".test-pyramid.yaml"}, {"commit", "-q", "-m", "pyramid"}} {
		if err := gitc(repo, args...); err != nil {
			return "", err
		}
	}
	return repo, nil
}

// seedGC030Attempt writes one attempt into root with a test-result at head, ending in
// the given terminal events (review, or review+done).
func seedGC030Attempt(root store.Root, repo, ticket, head string, done bool) error {
	if err := root.EnsureAttemptDirs(ticket, gc030Att); err != nil {
		return err
	}
	spec := "---\nid: " + ticket + "\ntitle: drvweb-030 " + ticket + "\n---\n\n# drvweb-030 " + ticket + "\n"
	if err := os.WriteFile(root.SpecPath(ticket), []byte(spec), 0o644); err != nil {
		return err
	}
	if err := attempt.WriteMeta(root, attempt.Meta{ID: gc030Att, Ticket: ticket, Repo: repo, Base: "main", Actor: "agent:acceptance"}); err != nil {
		return err
	}
	evs := []event.Event{
		{Type: "created", Actor: "agent:acceptance", Body: "start"},
		{Type: "test-result", Actor: "agent:acceptance", Rung: "acceptance", Commit: head},
		{Type: "review", Actor: "agent:acceptance", Body: "ready"},
	}
	if done {
		evs = append(evs, event.Event{Type: "done", Actor: "human:acceptance", Body: "ok"})
	}
	for _, e := range evs {
		if _, err := ticketlog.Append(root, ticket, gc030Att, e); err != nil {
			return err
		}
	}
	return nil
}

// serveGC030 starts the one long-lived server the scenario renders through.
func serveGC030(w *World, f *gc030, root store.Root) error {
	srv, err := web.New(root)
	if err != nil {
		return err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	f.board = &board{root: root, wd: f.repo, srv: ts}
	w.set("board", f.board)
	w.set("gc030", f)
	return nil
}

func newGC030Root(w *World) (store.Root, error) {
	dir, err := os.MkdirTemp("", "gc030-data-")
	if err != nil {
		return store.Root{}, err
	}
	w.defer_(func() { os.RemoveAll(dir) })
	return store.Root{Dir: dir}, nil
}

func revParse(dir, ref string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse %s in %s: %w", ref, dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func givenGC030ManyDone(w *World, sr *StepRun) error {
	repo, err := seedGC030Repo(w)
	if err != nil {
		return err
	}
	head, err := revParse(repo, "HEAD")
	if err != nil {
		return err
	}
	root, err := newGC030Root(w)
	if err != nil {
		return err
	}
	var refs strings.Builder
	for i := range 200 {
		ticket := fmt.Sprintf("GCD-%03d", i)
		fmt.Fprintf(&refs, "create refs/heads/draiver/%s/%s %s\n", ticket, gc030Att, head)
		if err := seedGC030Attempt(root, repo, ticket, head, true); err != nil {
			return err
		}
	}
	cmd := exec.Command("git", "-C", repo, "update-ref", "--stdin")
	cmd.Stdin = strings.NewReader(refs.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("update-ref: %v\n%s", err, out)
	}
	return serveGC030(w, &gc030{repo: repo}, root)
}

func gc030Get(f *gc030, path string) (string, time.Duration, error) {
	t0 := time.Now()
	resp, err := http.Get(f.board.url(path))
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	took := time.Since(t0)
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("GET %s: %d", path, resp.StatusCode)
	}
	return string(body), took, nil
}

func whenGC030ColdWarm(w *World, sr *StepRun) error {
	f, err := currentGC030(w)
	if err != nil {
		return err
	}
	if f.cold, _, err = gc030Get(f, "/board"); err != nil {
		return err
	}
	f.warm, f.warmTook, err = gc030Get(f, "/board")
	return err
}

// gc030Badges extracts every rendered pyramid badge element, in order.
var gc030BadgeRE = regexp.MustCompile(`<[^>]*data-testid="pyramid-[^"]*"[^>]*>`)

func thenGC030WarmSame(w *World, sr *StepRun) error {
	f, err := currentGC030(w)
	if err != nil {
		return err
	}
	cold, warm := gc030BadgeRE.FindAllString(f.cold, -1), gc030BadgeRE.FindAllString(f.warm, -1)
	if len(cold) < 200 {
		return fmt.Errorf("cold render shows %d pyramid badges, want 200", len(cold))
	}
	if strings.Join(cold, "\n") != strings.Join(warm, "\n") {
		return fmt.Errorf("warm render's badges differ from the cold render's (%d vs %d)", len(warm), len(cold))
	}
	return shoot(w, sr, "/", "drvweb030-warm-board", true, `[data-testid="board"]`, "")
}

func thenGC030WarmFast(w *World, sr *StepRun) error {
	f, err := currentGC030(w)
	if err != nil {
		return err
	}
	if f.warmTook >= 500*time.Millisecond {
		return fmt.Errorf("warm board render took %v, want well under 0.5s", f.warmTook)
	}
	fmt.Printf("drvweb-030: warm /board render over 200 Done attempts: %v\n", f.warmTook)
	return shoot(w, sr, "/", "drvweb030-warm-board-timed", true, `[data-testid="board"]`, "")
}

func givenGC030Branch(w *World, sr *StepRun) error {
	repo, err := seedGC030Repo(w)
	if err != nil {
		return err
	}
	branch := "draiver/" + gc030Ticket + "/" + gc030Att
	if err := gitc(repo, "branch", branch, "main"); err != nil {
		return err
	}
	head, err := revParse(repo, "refs/heads/"+branch)
	if err != nil {
		return err
	}
	root, err := newGC030Root(w)
	if err != nil {
		return err
	}
	if err := seedGC030Attempt(root, repo, gc030Ticket, head, false); err != nil {
		return err
	}
	return serveGC030(w, &gc030{repo: repo}, root)
}

func givenGC030Live(w *World, sr *StepRun) error {
	repo, err := seedGC030Repo(w)
	if err != nil {
		return err
	}
	m, err := worktree.NewManager(repo)
	if err != nil {
		return err
	}
	wt, err := m.Create(context.Background(), worktree.Spec{Key: worktree.Key{Ticket: gc030Ticket, Attempt: gc030Att}})
	if err != nil {
		return err
	}
	head, err := revParse(wt.Path, "HEAD")
	if err != nil {
		return err
	}
	root, err := newGC030Root(w)
	if err != nil {
		return err
	}
	if err := seedGC030Attempt(root, repo, gc030Ticket, head, false); err != nil {
		return err
	}
	return serveGC030(w, &gc030{repo: repo, checkout: wt.Path}, root)
}

// gc030State classifies the single attempt's badge on a rendered board.
func gc030State(body string) string {
	m := regexp.MustCompile(`<[^>]*data-testid="pyramid-` + gc030Ticket + `-` + gc030Att + `"[^>]*>`).FindString(body)
	switch {
	case m == "":
		return "none"
	case strings.Contains(m, `data-dirty="true"`):
		return "dirty"
	case strings.Contains(m, `data-stale="true"`):
		return "stale"
	case strings.Contains(m, "pyramid-green"):
		return "green"
	}
	return "unknown: " + m
}

func givenGC030Warm(w *World, sr *StepRun) error {
	f, err := currentGC030(w)
	if err != nil {
		return err
	}
	for range 2 { // cold, then warm
		body, _, err := gc030Get(f, "/board")
		if err != nil {
			return err
		}
		if got := gc030State(body); got != "green" {
			return fmt.Errorf("before the change: badge %q, want green", got)
		}
	}
	return nil
}

func whenGC030Commit(w *World, sr *StepRun) error {
	f, err := currentGC030(w)
	if err != nil {
		return err
	}
	ref := "refs/heads/draiver/" + gc030Ticket + "/" + gc030Att
	tip, err := revParse(f.repo, ref)
	if err != nil {
		return err
	}
	out, err := exec.Command("git", "-C", f.repo, "-c", "user.name=t", "-c", "user.email=t@t",
		"commit-tree", tip+"^{tree}", "-p", tip, "-m", "after the result").Output()
	if err != nil {
		return fmt.Errorf("commit-tree: %w", err)
	}
	return gitc(f.repo, "update-ref", ref, strings.TrimSpace(string(out)))
}

func whenGC030Dirty(w *World, sr *StepRun) error {
	f, err := currentGC030(w)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(f.checkout, "scratch.txt"), []byte("wip\n"), 0o644)
}

func whenGC030Reclaim(w *World, sr *StepRun) error {
	f, err := currentGC030(w)
	if err != nil {
		return err
	}
	if err := gitc(f.checkout, "commit", "-q", "--allow-empty", "-m", "after the result"); err != nil {
		return err
	}
	m, err := worktree.NewManager(f.repo)
	if err != nil {
		return err
	}
	return m.Remove(context.Background(), worktree.Key{Ticket: gc030Ticket, Attempt: gc030Att}, worktree.RemoveOptions{})
}

// thenGC030Badge renders the board (after wait — the poll gap, for dirtiness, which is
// sampled once per render) and asserts the attempt's badge state.
func thenGC030Badge(want string, wait time.Duration) StepFunc {
	return func(w *World, sr *StepRun) error {
		f, err := currentGC030(w)
		if err != nil {
			return err
		}
		time.Sleep(wait)
		body, _, err := gc030Get(f, "/board")
		if err != nil {
			return err
		}
		if got := gc030State(body); got != want {
			return fmt.Errorf("next render: badge %q, want %q", got, want)
		}
		return shoot(w, sr, "/", "drvweb030-badge-"+want, true, `[data-testid="board"]`, "")
	}
}
