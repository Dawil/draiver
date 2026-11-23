package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/worktree"
	"html"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/webshot"
)

// ---- fork-session.feature: copy a "fork this session" command (drvweb-029) ----

const forkSessionID = "0f1e2d3c-fork-demo-session"

func init() {
	drvweb029 := map[string]StepFunc{
		"a board attempt with a seeded claude-code session.json":                           givenForkSession,
		"a board attempt whose session.json has no session id":                             givenForkNoSessionID,
		"the attempt page shows the fork-session copy button":                              thenForkButtonShown,
		"the rendered fork command is a draiver fork of this attempt":                      thenForkCommandRendered,
		"a claude-code attempt whose worktree the daemon has reclaimed":                    givenForkReclaimed,
		"I run the fork command copied from the attempt page":                              whenRunCopiedFork,
		"the worktree is recreated on the attempt branch":                                  thenForkWorktreeRecreated,
		"claude is started there with --resume, --fork-session and --permission-mode plan": thenForkArgv,
		"the copy button sits inline in the Agent Logs section header":                     thenForkButtonInAgentLogs,
		"the attempt page shows no fork-session copy button":                               thenForkButtonAbsent,
	}
	for k, v := range drvweb029 {
		registry[k] = v
	}
}

// forkWorktree is the absolute worktree the seeded session.json names — a
// subdirectory with a space in it, so the screenshot shows the quoting at work.
func forkWorktree(b *board) string { return filepath.Join(b.wd, "my worktree") }

func seedForkSession(w *World, sid string) error {
	b, err := seedBoard(w, greenPyramid)
	if err != nil {
		return err
	}
	if err := b.root.EnsureSessionDir(accTicket, accAtt); err != nil {
		return err
	}
	data, err := json.MarshalIndent(session.Identity{
		Adapter: "claude-code", SessionID: sid, Worktree: forkWorktree(b),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(b.root.SessionMetaPath(accTicket, accAtt), data, 0o644); err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

func givenForkSession(w *World, sr *StepRun) error     { return seedForkSession(w, forkSessionID) }
func givenForkNoSessionID(w *World, sr *StepRun) error { return seedForkSession(w, "") }

func forkPage(w *World) (string, *board, error) {
	b, err := currentBoard(w)
	if err != nil {
		return "", nil, err
	}
	page, err := getBody(b.url("/ticket/" + accTicket + "/" + accAtt))
	return page, b, err
}

func thenForkButtonShown(w *World, sr *StepRun) error {
	page, _, err := forkPage(w)
	if err != nil {
		return err
	}
	for _, want := range []string{`data-testid="fork-copy"`, `data-testid="fork-command"`, "plan mode"} {
		if !strings.Contains(page, want) {
			return fmt.Errorf("attempt page missing %q", want)
		}
	}
	return nil
}

// forkCommandText extracts the unescaped text of the <code data-testid="fork-command">.
func forkCommandText(page string) (string, bool) {
	const open = `data-testid="fork-command">`
	i := strings.Index(page, open)
	if i < 0 {
		return "", false
	}
	rest := page[i+len(open):]
	j := strings.Index(rest, "</code>")
	if j < 0 {
		return "", false
	}
	return html.UnescapeString(rest[:j]), true
}

func thenForkCommandRendered(w *World, sr *StepRun) error {
	page, b, err := forkPage(w)
	if err != nil {
		return err
	}
	cmd, ok := forkCommandText(page)
	if !ok {
		return fmt.Errorf("no fork command <code> on the attempt page")
	}
	// The acceptance board's data root is a temp dir, not the CLI default, so the
	// command must carry it as --data for the paste to find this attempt.
	want := "draiver --data '" + b.root.Dir + "' fork '" + accTicket + "@" + accAtt + "'"
	if cmd != want {
		return fmt.Errorf("fork command = %q, want %q", cmd, want)
	}
	// Click the copy button and wait for its status feedback ("Copied", or the
	// manual-copy hint if headless Chromium refuses both clipboard paths), then shoot.
	return shootForkCopy(w, sr, b)
}

func thenForkButtonAbsent(w *World, sr *StepRun) error {
	page, _, err := forkPage(w)
	if err != nil {
		return err
	}
	if strings.Contains(page, `data-testid="fork-session"`) {
		return fmt.Errorf("fork-session button rendered for an attempt with no session id")
	}
	// Evidence: the same header with no copy button or command.
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt, "fork-session-absent", false, `[data-testid="ticket-detail"]`, "")
}

// shootForkCopy clicks the real copy button in a browser and waits for its status
// line to fill before capturing the header (button, feedback and command).
func shootForkCopy(w *World, sr *StepRun, b *board) error {
	return capture(sr, b.url("/ticket/"+accTicket+"/"+accAtt), "fork-session-copy", webshot.Options{
		Width: 1280, Height: 900,
		WaitSelector:           `[data-testid="fork-copy"]`,
		ClickSelector:          `[data-testid="fork-copy"]`,
		AfterClickWaitSelector: `.fork-copied:not(:empty)`,
	})
}

// thenForkButtonInAgentLogs pins the placement the reviewer asked for (decision
// #18): the button is inside the Agent Logs <summary> row, styled as the "Add
// entry" pill (compose-submit), not in the page header.
func thenForkButtonInAgentLogs(w *World, sr *StepRun) error {
	page, _, err := forkPage(w)
	if err != nil {
		return err
	}
	const open = `data-testid="agent-logs-summary"`
	i := strings.Index(page, open)
	if i < 0 {
		return fmt.Errorf("no Agent Logs summary on the attempt page")
	}
	j := strings.Index(page[i:], "</summary>")
	if j < 0 {
		return fmt.Errorf("unterminated Agent Logs summary")
	}
	summary := page[i : i+j]
	if !strings.Contains(summary, `data-testid="fork-copy"`) {
		return fmt.Errorf("fork-copy button is not inside the Agent Logs summary")
	}
	if !strings.Contains(summary, `class="compose-submit fork-copy"`) {
		return fmt.Errorf("fork-copy button does not use the Add entry (compose-submit) style")
	}
	if header, ok := sliceBetween(page, "<header", "</header>"); ok && strings.Contains(header, "fork-copy") {
		return fmt.Errorf("fork-copy button still rendered in the page header")
	}
	return nil
}

// ---- reclaimed-worktree scenario: run the copied command end to end ----

const forkReclaimedSID = "5e55-reclaimed-demo"

type forkRun struct {
	b        *board
	repo     string
	cacheDir string
	wtPath   string
	out      string
}

func givenForkReclaimed(w *World, sr *StepRun) error {
	tmp, err := os.MkdirTemp("", "acc-fork-")
	if err != nil {
		return err
	}
	w.defer_(func() { os.RemoveAll(tmp) })
	repo := filepath.Join(tmp, "repo")
	cacheDir := filepath.Join(tmp, "cache")
	dataDir := filepath.Join(tmp, "data")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.email=a@a", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %v\n%s", args, err, out)
		}
	}
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(accTicket, accAtt); err != nil {
		return err
	}
	spec := "---\nid: " + accTicket + "\ntitle: drvweb-029 fork\n---\n\n# drvweb-029 fork\n"
	if err := os.WriteFile(root.SpecPath(accTicket), []byte(spec), 0o644); err != nil {
		return err
	}
	meta := "---\nid: " + accAtt + "\nticket: " + accTicket + "\nrepo: " + repo + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(accTicket, accAtt), []byte(meta), 0o644); err != nil {
		return err
	}
	if _, err := ticketlog.Append(root, accTicket, accAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return err
	}

	// Cut the attempt's worktree exactly where the CLI (run with XDG_CACHE_HOME =
	// cacheDir) will look for it, record it in session.json, then reclaim it the
	// way the daemon does on retire: remove the checkout, keep the branch.
	base, err := worktree.DefaultBaseIn(cacheDir, repo)
	if err != nil {
		return err
	}
	wm, err := worktree.NewManager(repo, worktree.WithBase(base))
	if err != nil {
		return err
	}
	key := worktree.Key{Ticket: accTicket, Attempt: accAtt}
	wt, err := wm.Create(context.Background(), worktree.Spec{Key: key})
	if err != nil {
		return err
	}
	w.defer_(func() { wm.Remove(context.Background(), key, worktree.RemoveOptions{Force: true}) })
	if err := root.EnsureSessionDir(accTicket, accAtt); err != nil {
		return err
	}
	data, _ := json.Marshal(session.Identity{Adapter: "claude-code", SessionID: forkReclaimedSID, Worktree: wt.Path})
	if err := os.WriteFile(root.SessionMetaPath(accTicket, accAtt), data, 0o644); err != nil {
		return err
	}
	if err := wm.Remove(context.Background(), key, worktree.RemoveOptions{Force: true}); err != nil {
		return err
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		return fmt.Errorf("worktree %s still present after reclaim", wt.Path)
	}

	srv, err := web.New(root)
	if err != nil {
		return err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	b := &board{root: root, wd: repo, srv: ts}
	w.set("board", b)
	w.set("forkRun", &forkRun{b: b, repo: repo, cacheDir: cacheDir, wtPath: wt.Path})
	// Evidence: the page still offers the fork even though the checkout is gone.
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt, "fork-session-reclaimed", false, `[data-testid="fork-copy"]`, "")
}

func currentForkRun(w *World) (*forkRun, error) {
	fr, _ := w.get("forkRun").(*forkRun)
	if fr == nil {
		return nil, fmt.Errorf("no reclaimed-fork fixture in scope")
	}
	return fr, nil
}

// whenRunCopiedFork runs the exact text the page renders — through a shell, with
// the freshly built draiver first on PATH — plus --print, so the verb reports the
// cwd and claude argv instead of exec'ing a claude that isn't installed here.
func whenRunCopiedFork(w *World, sr *StepRun) error {
	fr, err := currentForkRun(w)
	if err != nil {
		return err
	}
	page, err := getBody(fr.b.url("/ticket/" + accTicket + "/" + accAtt))
	if err != nil {
		return err
	}
	cmdText, ok := forkCommandText(page)
	if !ok {
		return fmt.Errorf("no fork command on the attempt page")
	}
	bin, err := overlayBin()
	if err != nil {
		return err
	}
	binDir := filepath.Join(filepath.Dir(bin), "fork-path")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	link := filepath.Join(binDir, "draiver")
	os.Remove(link)
	if err := os.Symlink(bin, link); err != nil {
		return err
	}
	sh := exec.Command("sh", "-c", cmdText+" --print")
	var env []string
	for _, e := range os.Environ() {
		k := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			k = e[:i]
		}
		switch k {
		case "PATH", "XDG_CACHE_HOME", "DRAIVER_DATA", "DRAIVER_ATTEMPT", "DRAIVER_CONFIG":
			continue
		}
		env = append(env, e)
	}
	sh.Env = append(env, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "XDG_CACHE_HOME="+fr.cacheDir)
	out, err := sh.CombinedOutput()
	fr.out = string(out)
	if err != nil {
		return fmt.Errorf("copied fork command failed: %v\n%s", err, out)
	}
	return nil
}

func thenForkWorktreeRecreated(w *World, sr *StepRun) error {
	fr, err := currentForkRun(w)
	if err != nil {
		return err
	}
	if !strings.Contains(fr.out, "recreated "+fr.wtPath) {
		return fmt.Errorf("fork did not report recreating %s:\n%s", fr.wtPath, fr.out)
	}
	head, err := exec.Command("git", "-C", fr.wtPath, "branch", "--show-current").Output()
	if err != nil {
		return fmt.Errorf("recreated worktree %s is not a checkout: %v", fr.wtPath, err)
	}
	if got, want := strings.TrimSpace(string(head)), "draiver/"+accTicket+"/"+accAtt; got != want {
		return fmt.Errorf("recreated worktree on branch %q, want %q", got, want)
	}
	return nil
}

func thenForkArgv(w *World, sr *StepRun) error {
	fr, err := currentForkRun(w)
	if err != nil {
		return err
	}
	for _, want := range []string{
		"cwd: " + fr.wtPath + "\n",
		"argv: claude --resume " + forkReclaimedSID + " --fork-session --permission-mode plan",
	} {
		if !strings.Contains(fr.out, want) {
			return fmt.Errorf("fork --print output missing %q:\n%s", want, fr.out)
		}
	}
	return nil
}
