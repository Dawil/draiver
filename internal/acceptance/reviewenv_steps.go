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
	"strconv"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/reviewenv"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
)

// This file is drv-020's slice of the acceptance suite: the Gherkin in
// features/review_env.feature exercised against the *real* review-env lifecycle
// (internal/reviewenv) over a live webui, so the report's screenshots are of a
// genuinely running instance of the feature under review — "click around the working
// thing," captured. It plugs into the same runner-agnostic registry drv-019's
// download/rerun steps use (init() extends the shared map), so these scenarios ride
// the identical cucumber-JSON + PNG-embedding pipeline the acceptance rung captures.
func init() {
	for text, fn := range map[string]StepFunc{
		// --- features/review_env.feature ---
		"a Review attempt whose repo configures a review-ready environment":        givenReviewReadyAttempt,
		"a running review environment":                                             givenRunningEnv,
		"a running review environment whose teardown does not stop it":             givenLeakingEnv,
		"a Review attempt whose reviewEnvironment names an incomplete environment": givenIncompleteEnv,
		"I open the attempt page":                                                  whenOpenAttemptPage,
		"I launch the review environment":                                          whenLaunchReviewEnv,
		"I tear the review environment down":                                       whenTearReviewEnvDown,
		"the review-environment panel offers to launch it":                         thenPanelOffersLaunch,
		"the environment comes up and the attempt page reveals a clickable URL":    thenUpRevealsURL,
		"the confirm-down healthcheck is red and the panel returns to rest":        thenTornDownToRest,
		"the panel warns of a leak rather than reporting the environment gone":     thenLeakWarned,
		"the panel shows a misconfiguration note and offers no launch":             thenMisconfigNote,
	} {
		if _, clash := registry[text]; clash {
			panic("acceptance: duplicate step registration: " + text)
		}
		registry[text] = fn
	}
}

const (
	revTicket = "ACC-REV"
	revAtt    = "0001"
	revEnv    = "review"
)

// reviewScripts are the repo-under-review's committed env scripts. They stand up a
// *real* local HTTP server on the injected DRAIVER_REVIEW_PORT, so the healthcheck
// genuinely probes a live socket and the revealed URL is genuinely clickable — the
// faithful "this working in a test environment" the resolution asks for.
var reviewScripts = map[string]string{
	// The service under review: a minimal HTTP server bound to the injected port, so
	// the review URL the reviewer clicks actually answers.
	"review-server.js": `const http = require('http');
const fs = require('fs');
const port = process.env.DRAIVER_REVIEW_PORT;
const instance = process.env.DRAIVER_REVIEW_INSTANCE || 'unknown';
const srv = http.createServer(function (req, res) {
  res.writeHead(200, { 'content-type': 'text/html' });
  res.end('<!doctype html><title>Review env</title><h1>Review environment is up</h1>' +
    '<p>instance ' + instance + ' on port ' + port + '</p>');
});
srv.listen(port, '127.0.0.1', function () {
  fs.writeFileSync('server.pid', String(process.pid));
});
process.on('SIGTERM', function () {
  try { fs.unlinkSync('server.pid'); } catch (e) {}
  process.exit(0);
});
`,
	// up: start the server in the background with its output redirected to a file (so
	// draiver's CombinedOutput does not block on the inherited pipe), then wait until
	// the port actually accepts a connection before returning — so confirm-up never
	// races bring-up.
	"up.sh": `node review-server.js >review-server.log 2>&1 &
i=0
while [ "$i" -lt 100 ]; do
  if node -e "require('net').connect(Number(process.env.DRAIVER_REVIEW_PORT),'127.0.0.1').on('connect',function(){process.exit(0)}).on('error',function(){process.exit(1)})" 2>/dev/null; then
    echo "up: listening on $DRAIVER_REVIEW_PORT"
    exit 0
  fi
  i=$((i+1))
  sleep 0.1
done
echo "up: server never listened on $DRAIVER_REVIEW_PORT" >&2
cat review-server.log >&2 || true
exit 1
`,
	// healthcheck: a real HTTP GET — 200 is green, a refused connection is red. This is
	// the same probe draiver runs green-expected on confirm-up and red-expected on
	// confirm-down, closing the teardown loop deterministically.
	"healthcheck.sh": `node -e "var h=require('http');h.get('http://127.0.0.1:'+process.env.DRAIVER_REVIEW_PORT+'/',function(r){process.exit(r.statusCode===200?0:1)}).on('error',function(){process.exit(1)})"
`,
	// down: stop the server and wait until the port is released, so confirm-down sees it
	// gone.
	"down.sh": `if [ -f server.pid ]; then
  kill "$(cat server.pid)" 2>/dev/null || true
fi
i=0
while [ "$i" -lt 100 ]; do
  if node -e "require('net').connect(Number(process.env.DRAIVER_REVIEW_PORT),'127.0.0.1').on('connect',function(){process.exit(0)}).on('error',function(){process.exit(1)})" 2>/dev/null; then
    i=$((i+1)); sleep 0.1
  else
    rm -f server.pid
    echo "down: port $DRAIVER_REVIEW_PORT released"
    exit 0
  fi
done
echo "down: port still answering after kill" >&2
exit 0
`,
	// down-noop: the leak fixture — "down" ran but deliberately leaves the service
	// answering, so confirm-down's healthcheck stays green and draiver flags a leak
	// rather than silently assuming teardown worked.
	"down-noop.sh": `echo "down: intentionally leaves the service running (leak scenario)"
exit 0
`,
}

// A review-ready pyramid: up + down + a real healthcheck, with an agent-authored URL
// template draiver expands with the injected port. down.sh genuinely stops the server.
const reviewReadyPyramid = `environments:
  - name: review
    up: sh up.sh
    down: sh down.sh
    healthchecks:
      - name: http-ready
        script: sh healthcheck.sh
    url: http://127.0.0.1:${DRAIVER_REVIEW_PORT}/
levels:
  - name: check
    run: "true"
`

// The leak pyramid is review-ready too, but its down is a no-op — so teardown's
// confirm-down healthcheck stays green and the lifecycle reaches teardown-failed.
const leakPyramid = `environments:
  - name: review
    up: sh up.sh
    down: sh down-noop.sh
    healthchecks:
      - name: http-ready
        script: sh healthcheck.sh
    url: http://127.0.0.1:${DRAIVER_REVIEW_PORT}/
levels:
  - name: check
    run: "true"
`

// The incomplete pyramid names an environment that is NOT review-ready (no
// healthcheck), so the panel must render a loud misconfiguration note rather than
// vanishing.
const incompletePyramid = `environments:
  - name: review
    up: sh up.sh
    down: sh down.sh
levels:
  - name: check
    run: "true"
`

// reviewBoard carries everything a review-env scenario drives: the live webui board,
// the lifecycle Manager (its worktree base pointed at a temp dir), the Review-state
// attempt, and the resolved environment.
type reviewBoard struct {
	b    *board
	mgr  *reviewenv.Manager
	att  project.Attempt
	env  *pyramid.Environment
	repo string
}

func currentReviewBoard(w *World) (*reviewBoard, error) {
	rb, _ := w.get("reviewBoard").(*reviewBoard)
	if rb == nil {
		return nil, errors.New("no review board set up in scope")
	}
	return rb, nil
}

// seedReviewBoard builds a self-contained review-env fixture: a throwaway git repo
// whose branch carries the given pyramid + the committed env scripts, a data root
// with a Review-state attempt pointing at it, a temp config setting the repo's
// reviewEnvironment, and a live webui over that root. All temp state, the config env
// var, and the server are registered for cleanup on the World.
func seedReviewBoard(w *World, pyramidYAML string) (*reviewBoard, error) {
	ctx := context.Background()

	// 1. The repo under review: a real git repo with the pyramid + scripts committed,
	//    plus the attempt branch draiver/<ticket>/<attempt> the launcher reviews.
	repo, err := os.MkdirTemp("", "acc-rev-repo-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(repo) })
	files := map[string]string{".test-pyramid.yaml": pyramidYAML}
	for name, body := range reviewScripts {
		files[name] = body
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			return nil, err
		}
	}
	for _, args := range [][]string{
		{"-c", "init.defaultBranch=main", "init", "-q", repo},
		{"-C", repo, "add", "-A"},
		{"-C", repo, "-c", "user.email=acc@draiver.test", "-c", "user.name=acc", "commit", "-q", "-m", "review env fixture"},
		{"-C", repo, "branch", "draiver/" + revTicket + "/" + revAtt},
	} {
		if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
		}
	}

	// 2. The data root with a Review-state attempt pointing at the repo.
	dataDir, err := os.MkdirTemp("", "acc-rev-data-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(dataDir) })
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(revTicket, revAtt); err != nil {
		return nil, err
	}
	spec := "---\nid: " + revTicket + "\ntitle: review env acceptance\n---\n\n# review env acceptance\n"
	if err := os.WriteFile(root.SpecPath(revTicket), []byte(spec), 0o644); err != nil {
		return nil, err
	}
	meta := "---\nid: " + revAtt + "\nticket: " + revTicket + "\nrepo: " + repo + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(revTicket, revAtt), []byte(meta), 0o644); err != nil {
		return nil, err
	}
	// created + review → project.Derive lands the attempt at Review, the panel's gate.
	for _, ev := range []event.Event{
		{Type: "created", Actor: "agent:acceptance", Body: "start"},
		{Type: "review", Actor: "agent:acceptance", Body: "claiming review"},
	} {
		if _, err := ticketlog.Append(root, revTicket, revAtt, ev); err != nil {
			return nil, err
		}
	}

	// 3. A temp config whose repo entry sets reviewEnvironment, pointed at via
	//    DRAIVER_CONFIG so the webui's visibility gate (config.Load("")) holds. The old
	//    value is restored on cleanup so later features see the ambient config.
	cfgPath := filepath.Join(dataDir, "config.json")
	oldCfg, hadCfg := os.LookupEnv("DRAIVER_CONFIG")
	if err := os.Setenv("DRAIVER_CONFIG", cfgPath); err != nil {
		return nil, err
	}
	w.defer_(func() {
		if hadCfg {
			os.Setenv("DRAIVER_CONFIG", oldCfg)
		} else {
			os.Unsetenv("DRAIVER_CONFIG")
		}
	})
	envName := revEnv
	if _, err := config.SetRepoSettings("", repo, config.RepoSettingsUpdate{ReviewEnvironment: &envName}); err != nil {
		return nil, err
	}

	// 4. The live webui over that root, and the lifecycle Manager whose review
	//    worktrees land under a temp base (never the user cache).
	srv, err := web.New(root)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)

	base, err := os.MkdirTemp("", "acc-rev-wt-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(base) })
	mgr := reviewenv.New(reviewenv.Options{Root: root, Base: base})

	att, err := project.LoadAttempt(root, revTicket, revAtt)
	if err != nil {
		return nil, err
	}
	p, err := pyramid.Parse([]byte(pyramidYAML))
	if err != nil {
		return nil, err
	}
	env, _ := p.EnvironmentByName(revEnv)

	bd := &board{root: root, wd: repo, srv: ts}
	rb := &reviewBoard{b: bd, mgr: mgr, att: att, env: env, repo: repo}
	w.set("board", bd) // so shoot() finds the live server
	w.set("reviewBoard", rb)
	return rb, nil
}

// launchReviewEnv drives the real lifecycle to Up: Manager.Launch cuts the detached
// review worktree at the reviewed commit, runs up (a real server), confirms the
// healthcheck green, and reveals the URL. It registers a force-kill cleanup so no
// node server or port leaks across the single-process suite, whatever the scenario
// asserts.
func launchReviewEnv(w *World) error {
	rb, err := currentReviewBoard(w)
	if err != nil {
		return err
	}
	if rb.env == nil {
		return errors.New("no review environment resolved from the pyramid")
	}
	// Always-run teardown of whatever this launch stood up (the leak scenario's down is
	// a no-op, so without this its server would outlive the scenario).
	w.defer_(func() { forceKillReviewEnv(rb) })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rec, err := rb.mgr.Launch(ctx, rb.att, rb.env)
	if err != nil {
		return fmt.Errorf("launch errored: %w", err)
	}
	if rec.State != reviewenv.Up {
		return fmt.Errorf("launch state = %q, want up (message: %s)", rec.State, rec.Message)
	}
	if strings.TrimSpace(rec.URL) == "" {
		return errors.New("up but no URL revealed")
	}
	w.set("revrec", rec)
	return nil
}

// forceKillReviewEnv is the scenario's safety net: kill any server the review worktree
// left running (by its pid file) and force-remove the worktree, so a leak scenario —
// or an assertion that fails before teardown — leaves no live process or busy port.
func forceKillReviewEnv(rb *reviewBoard) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if rec, ok, err := rb.mgr.Load(rb.att.Ticket, rb.att.ID); err == nil && ok && rec.Worktree != "" {
		if data, err := os.ReadFile(filepath.Join(rec.Worktree, "server.pid")); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				if proc, err := os.FindProcess(pid); err == nil {
					_ = proc.Kill()
				}
			}
		}
		exec.CommandContext(ctx, "git", "-C", rb.repo, "worktree", "remove", "--force", rec.Worktree).Run()
	}
}

// ---- Given ----

func givenReviewReadyAttempt(w *World, sr *StepRun) error {
	_, err := seedReviewBoard(w, reviewReadyPyramid)
	return err
}

func givenRunningEnv(w *World, sr *StepRun) error {
	if _, err := seedReviewBoard(w, reviewReadyPyramid); err != nil {
		return err
	}
	return launchReviewEnv(w)
}

func givenLeakingEnv(w *World, sr *StepRun) error {
	if _, err := seedReviewBoard(w, leakPyramid); err != nil {
		return err
	}
	return launchReviewEnv(w)
}

func givenIncompleteEnv(w *World, sr *StepRun) error {
	_, err := seedReviewBoard(w, incompletePyramid)
	return err
}

// ---- When ----

func whenOpenAttemptPage(w *World, sr *StepRun) error {
	rb, err := currentReviewBoard(w)
	if err != nil {
		return err
	}
	resp, err := http.Get(rb.b.url("/ticket/" + revTicket + "/" + revAtt))
	if err != nil {
		return fmt.Errorf("GET attempt page: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("attempt page status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	w.set("page", string(body))
	return nil
}

func whenLaunchReviewEnv(w *World, sr *StepRun) error {
	return launchReviewEnv(w)
}

func whenTearReviewEnvDown(w *World, sr *StepRun) error {
	rb, err := currentReviewBoard(w)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rec, err := rb.mgr.Teardown(ctx, rb.att, "button")
	if err != nil {
		return fmt.Errorf("teardown errored: %w", err)
	}
	w.set("revrec", rec)
	return nil
}

// ---- Then (each attaches the scenario's screenshot of the live panel) ----

func thenPanelOffersLaunch(w *World, sr *StepRun) error {
	html, _ := w.get("page").(string)
	if !strings.Contains(html, `data-testid="review-env"`) {
		return errors.New("attempt page has no review-env panel")
	}
	if !strings.Contains(html, `data-testid="action-review-env-up"`) {
		return errors.New("review-env panel offers no Launch button")
	}
	return shoot(w, sr, "/ticket/"+revTicket+"/"+revAtt, "review-env-offered", true, `[data-testid="attempt-tab-review-env"]`, `[data-testid="attempt-tab-review-env"]`)
}

func thenUpRevealsURL(w *World, sr *StepRun) error {
	rec, _ := w.get("revrec").(reviewenv.Record)
	if rec.State != reviewenv.Up || rec.URL == "" {
		return fmt.Errorf("record not up-with-url: state=%q url=%q", rec.State, rec.URL)
	}
	// The live panel must now carry the clickable URL — revealed only on green.
	rb, err := currentReviewBoard(w)
	if err != nil {
		return err
	}
	resp, err := http.Get(rb.b.url("/ticket/" + revTicket + "/" + revAtt))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, `data-testid="review-env-url"`) {
		return errors.New("up panel reveals no clickable URL")
	}
	if !strings.Contains(html, rec.URL) {
		return fmt.Errorf("panel does not carry the revealed URL %q", rec.URL)
	}
	return shoot(w, sr, "/ticket/"+revTicket+"/"+revAtt, "review-env-up-url", true, `[data-testid="attempt-tab-review-env"]`, `[data-testid="attempt-tab-review-env"]`)
}

func thenTornDownToRest(w *World, sr *StepRun) error {
	rec, _ := w.get("revrec").(reviewenv.Record)
	if rec.State != reviewenv.Down {
		return fmt.Errorf("teardown state = %q, want down (message: %s)", rec.State, rec.Message)
	}
	return shoot(w, sr, "/ticket/"+revTicket+"/"+revAtt, "review-env-torn-down", true, `[data-testid="attempt-tab-review-env"]`, `[data-testid="attempt-tab-review-env"]`)
}

func thenLeakWarned(w *World, sr *StepRun) error {
	rec, _ := w.get("revrec").(reviewenv.Record)
	if rec.State != reviewenv.TeardownFailed {
		return fmt.Errorf("teardown state = %q, want teardown-failed (leak)", rec.State)
	}
	if !strings.Contains(strings.ToLower(rec.Message), "leak") {
		return fmt.Errorf("leak message does not explain the leak: %q", rec.Message)
	}
	return shoot(w, sr, "/ticket/"+revTicket+"/"+revAtt, "review-env-leak", true, `[data-testid="attempt-tab-review-env"]`, `[data-testid="attempt-tab-review-env"]`)
}

func thenMisconfigNote(w *World, sr *StepRun) error {
	html, _ := w.get("page").(string)
	if !strings.Contains(html, `data-testid="review-env-misconfigured"`) {
		return errors.New("misconfigured review env renders no diagnostic note")
	}
	if strings.Contains(html, `data-testid="action-review-env-up"`) {
		return errors.New("a misconfigured review env must offer no Launch button")
	}
	return shoot(w, sr, "/ticket/"+revTicket+"/"+revAtt, "review-env-misconfigured", true, `[data-testid="attempt-tab-review-env"]`, `[data-testid="attempt-tab-review-env"]`)
}
