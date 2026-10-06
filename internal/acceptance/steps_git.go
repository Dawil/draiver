package acceptance

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/webshot"
	"github.com/Dawil/draiver/internal/worktree"
)

// steps_git.go realises drv-022's acceptance criteria
// (features/git_controls_sync.feature) against a live internal/web server backed by
// a real git repo + managed worktree + origin remote. Each Sync scenario performs
// the actual button click in a browser — accepting the hx-confirm — so the captured
// screenshot shows the genuine outcome banner the user sees; the position scenario
// renders the panel for real so the branch + ahead/behind line is live git output,
// not a fixture. The drv-022 steps register themselves into the shared registry from
// an init(), leaving the earlier steps untouched.

const (
	gitTicket = "GIT-1"
	gitAtt    = "0001"
)

func init() {
	drv022 := map[string]StepFunc{
		"a Running attempt whose base has advanced past the branch":  givenGitAdvanced,
		"a Running attempt already up to date with its remote":       givenGitUpToDate,
		"a Running attempt whose checkout is dirty":                  givenGitDirty,
		"a Running attempt ahead of its primary remote":              givenGitAhead,
		"I Sync from the Git controls panel":                         whenSyncFromPanel,
		"the Sync reports a success outcome in the panel":            thenSyncSuccess,
		"the Sync reports an already-up-to-date no-op in the panel":  thenSyncNoop,
		"the Sync reports a failure outcome in the panel without a 500": thenSyncFailure,
		"the Git controls panel shows the current branch and how far ahead/behind the remote it is": thenPositionShown,
	}
	for text, fn := range drv022 {
		registry[text] = fn
	}
}

// gitBoard is a board plus the on-disk git facts a drv-022 scenario needs to drive
// and verify a real Sync: the backing repo and the attempt's checkout path.
type gitBoard struct {
	*board
	repo     string
	checkout string
	variant  string
}

// gitc runs a git command in dir, failing the scenario (via a returned error) on a
// nonzero exit — the acceptance-side sibling of the web tests' gitInWeb.
func gitc(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return nil
}

// seedGitBoard builds a live board over a Running GIT-1/0001 attempt backed by a real
// repo + managed worktree + bare origin, shaped to variant:
//   - "advanced"  local base moved past the branch → Sync pulls/back-merges (success)
//   - "uptodate"  nothing has moved → Sync is an explicit no-op
//   - "dirty"     the checkout holds uncommitted changes → Sync fails
//   - "ahead"     the branch is one commit ahead of its pushed remote counterpart
//
// XDG_CACHE_HOME is redirected to a scenario-scoped temp dir (restored on cleanup)
// so the managed worktree the web layer resolves is the one seeded here, and no real
// cache is touched. Scenarios run sequentially, so the env swap is race-free.
func seedGitBoard(w *World, variant string) (*gitBoard, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not on PATH")
	}
	cache, err := os.MkdirTemp("", "git-cache-")
	if err != nil {
		return nil, err
	}
	prev, had := os.LookupEnv("XDG_CACHE_HOME")
	os.Setenv("XDG_CACHE_HOME", cache)
	w.defer_(func() {
		if had {
			os.Setenv("XDG_CACHE_HOME", prev)
		} else {
			os.Unsetenv("XDG_CACHE_HOME")
		}
		os.RemoveAll(cache)
	})

	repo, err := os.MkdirTemp("", "git-repo-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(repo) })
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if err := gitc(repo, args...); err != nil {
			return nil, err
		}
	}

	bare, err := os.MkdirTemp("", "git-bare-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(bare) })
	if err := gitc(bare, "init", "-q", "--bare", "-b", "main"); err != nil {
		return nil, err
	}
	if err := gitc(repo, "remote", "add", "origin", bare); err != nil {
		return nil, err
	}
	if err := gitc(repo, "push", "-q", "origin", "main"); err != nil {
		return nil, err
	}

	// Cut the attempt's managed worktree + branch and put a commit on it, exactly as
	// the daemon would. The web layer resolves this same worktree (same default base
	// under the redirected cache), so a Sync / position read operates on it.
	m, err := worktree.NewManager(repo)
	if err != nil {
		return nil, err
	}
	key := worktree.Key{Ticket: gitTicket, Attempt: gitAtt}
	wt, err := m.Create(context.Background(), worktree.Spec{Key: key})
	if err != nil {
		return nil, err
	}
	checkout := wt.Path
	if err := os.WriteFile(filepath.Join(checkout, "feat.txt"), []byte("feature"), 0o644); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"add", "feat.txt"}, {"commit", "-q", "-m", "feat"}} {
		if err := gitc(checkout, args...); err != nil {
			return nil, err
		}
	}

	switch variant {
	case "advanced":
		// Local base advances past the branch, so Sync has something to back-merge.
		if err := gitc(repo, "commit", "-q", "--allow-empty", "-m", "base moved"); err != nil {
			return nil, err
		}
	case "uptodate":
		// Nothing moves: origin/main == local main, and the branch already contains
		// main → Sync is a pure no-op.
	case "dirty":
		// An uncommitted change in the checkout makes the back-merge refuse.
		if err := os.WriteFile(filepath.Join(checkout, "scratch.txt"), []byte("wip"), 0o644); err != nil {
			return nil, err
		}
	case "ahead":
		// Push the branch so it has a remote counterpart, then add one local commit:
		// the branch is ahead 1 / behind 0 of origin/<branch>.
		if err := gitc(checkout, "push", "-q", "origin", "HEAD:refs/heads/"+wt.Branch); err != nil {
			return nil, err
		}
		if err := gitc(checkout, "commit", "-q", "--allow-empty", "-m", "ahead one"); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown git board variant %q", variant)
	}

	// Seed the Running attempt on disk (spec + attempt.md recording repo+base + a
	// created event) and stand up a live web server over it.
	dataDir, err := os.MkdirTemp("", "git-data-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(dataDir) })
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(gitTicket, gitAtt); err != nil {
		return nil, err
	}
	spec := "---\nid: " + gitTicket + "\ntitle: drv-022 Git controls\n---\n\n# drv-022 Git controls\n\n## Acceptance criteria\n\n- Sync always shows a result.\n"
	if err := os.WriteFile(root.SpecPath(gitTicket), []byte(spec), 0o644); err != nil {
		return nil, err
	}
	meta := "---\nid: " + gitAtt + "\nticket: " + gitTicket + "\nrepo: " + repo + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(gitTicket, gitAtt), []byte(meta), 0o644); err != nil {
		return nil, err
	}
	if _, err := ticketlog.Append(root, gitTicket, gitAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return nil, err
	}

	srv, err := web.New(root)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	return &gitBoard{board: &board{root: root, wd: repo, srv: ts}, repo: repo, checkout: checkout, variant: variant}, nil
}

func setGitBoard(w *World, variant string) error {
	b, err := seedGitBoard(w, variant)
	if err != nil {
		return err
	}
	w.set("gitboard", b)
	return nil
}

func givenGitAdvanced(w *World, sr *StepRun) error { return setGitBoard(w, "advanced") }
func givenGitUpToDate(w *World, sr *StepRun) error { return setGitBoard(w, "uptodate") }
func givenGitDirty(w *World, sr *StepRun) error    { return setGitBoard(w, "dirty") }
func givenGitAhead(w *World, sr *StepRun) error    { return setGitBoard(w, "ahead") }

func currentGitBoard(w *World) (*gitBoard, error) {
	b, _ := w.get("gitboard").(*gitBoard)
	if b == nil {
		return nil, errors.New("no git board set up in scope")
	}
	return b, nil
}

// whenSyncFromPanel drives the real Sync: in a live browser it selects the Git
// controls tab, clicks the Sync button, accepts the hx-confirm, waits for the result
// banner to swap in, and screenshots the panel. The banner carries data-kind only
// after the swap, so waiting on it both lets the POST finish (its durable effect is
// then on disk for the Then step) and guarantees the shot shows the outcome.
func whenSyncFromPanel(w *World, sr *StepRun) error {
	b, err := currentGitBoard(w)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	png, err := webshot.Capture(ctx, b.url("/ticket/"+gitTicket+"/"+gitAtt), webshot.Options{
		Width: 1280, Height: 1000, FullPage: true,
		ClickSelector:          `[data-testid="attempt-tab-git"]`,
		ClickSelector2:         `[data-testid="action-git-sync"]`,
		AfterClickWaitSelector: `#git-result[data-kind]`,
		AcceptDialogs:          true,
	})
	if err != nil {
		return err
	}
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png",
		Name:     "sync-" + b.variant,
		Data:     base64.StdEncoding.EncodeToString(png),
	})
	return nil
}

// notesCount returns how many `note` events the attempt has recorded — the durable
// proof a Sync moved something (a success records one; a no-op and a failure record
// none).
func notesCount(b *gitBoard) (int, error) {
	events, err := ticketlog.Read(b.root, gitTicket, gitAtt)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range events {
		if e.Type == "note" {
			n++
		}
	}
	return n, nil
}

func thenSyncSuccess(w *World, sr *StepRun) error {
	b, err := currentGitBoard(w)
	if err != nil {
		return err
	}
	// A real pull/back-merge records exactly one durable note.
	if n, err := notesCount(b); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("a successful Sync should record one note, got %d", n)
	}
	return assertNonTerminal(b)
}

func thenSyncNoop(w *World, sr *StepRun) error {
	b, err := currentGitBoard(w)
	if err != nil {
		return err
	}
	// Nothing moved, so nothing is recorded — the banner is an explicit no-op success.
	if n, err := notesCount(b); err != nil {
		return err
	} else if n != 0 {
		return fmt.Errorf("a no-op Sync should record no note, got %d", n)
	}
	return assertNonTerminal(b)
}

func thenSyncFailure(w *World, sr *StepRun) error {
	b, err := currentGitBoard(w)
	if err != nil {
		return err
	}
	// The dirty checkout made the back-merge refuse: the failure is surfaced in the
	// banner (not a 500), nothing is recorded, and the attempt is untouched.
	if n, err := notesCount(b); err != nil {
		return err
	} else if n != 0 {
		return fmt.Errorf("a failed Sync should record no note, got %d", n)
	}
	// The GET page still serves 200 (the panel is intact, not a crashed 500).
	resp, err := http.Get(b.url("/ticket/" + gitTicket + "/" + gitAtt))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("attempt page status = %d, want 200 (panel must survive a failed Sync)", resp.StatusCode)
	}
	return assertNonTerminal(b)
}

// assertNonTerminal confirms the Git controls are non-terminal: the attempt stays
// Running after a Sync, whatever the outcome.
func assertNonTerminal(b *gitBoard) error {
	a, err := project.LoadAttempt(b.root, gitTicket, gitAtt)
	if err != nil {
		return err
	}
	if a.State != project.Running {
		return fmt.Errorf("Sync is non-terminal, but the attempt is %q (want Running)", a.State)
	}
	return nil
}

func thenPositionShown(w *World, sr *StepRun) error {
	b, err := currentGitBoard(w)
	if err != nil {
		return err
	}
	resp, err := http.Get(b.url("/ticket/" + gitTicket + "/" + gitAtt))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	body := string(raw)
	if !strings.Contains(body, `data-testid="git-position"`) {
		return errors.New("Git controls panel is missing the position line")
	}
	// The current checked-out branch — the attempt branch, distinct from the base.
	wantBranch := "draiver/" + gitTicket + "/" + gitAtt
	if !strings.Contains(body, wantBranch) {
		return fmt.Errorf("position line does not show the current branch %q", wantBranch)
	}
	// The branch was seeded one commit ahead of its pushed remote counterpart.
	if !strings.Contains(body, "ahead 1 · behind 0") {
		return errors.New("position line does not show 'ahead 1 · behind 0' versus the primary remote")
	}
	// Screenshot the revealed panel (select the Git controls tab first).
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	png, err := webshot.Capture(ctx, b.url("/ticket/"+gitTicket+"/"+gitAtt), webshot.Options{
		Width: 1280, Height: 1000, FullPage: true,
		ClickSelector:          `[data-testid="attempt-tab-git"]`,
		AfterClickWaitSelector: `[data-testid="git-position"]`,
	})
	if err != nil {
		return err
	}
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png",
		Name:     "git-position-ahead",
		Data:     base64.StdEncoding.EncodeToString(png),
	})
	return nil
}
