package acceptance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/overlay"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/reviewenv"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/worktree"
)

// steps_reviewenv_overlay_drv053.go realises drv-053's acceptance criteria
// (features/attempt-page/review-env-overlay.feature): a review environment launched
// from the web UI for an overlay-only codebase honours the drv-041 overlay execution
// contract — DRAIVER_CODEBASE_DIR / DRAIVER_OVERLAY_DIR exported into up, down, and
// every healthcheck — resolves its definition and scripts from a detached overlay
// checkout at the attempt's overlay-branch tip (never overlay main or its WIP), and
// leaks nothing on teardown or a failed `up`.
//
// Unlike the drv-020 review-env steps (which drive reviewenv.Manager.Launch directly),
// these go through the REAL webui handlers — POST …/review-env/{env}/up and …/down — so
// the whole panel → gate → handler → Launch threading of the pinned overlay revision is
// under test. Every script records the env it saw into a proof dir ($DRV053_PROOF),
// which the Then steps read and attach as text evidence.

const (
	d53Ticket = "OVL-53"
	d53Att    = "0001"
	d53Proof  = "DRV053_PROOF"
)

func init() {
	for text, fn := range map[string]StepFunc{
		"an overlay-only codebase in Review whose overlay declares its review env via $DRAIVER_OVERLAY_DIR":                                             d53GivenOverlayCodebase,
		"the attempt's overlay branch changes the review env's healthcheck and URL":                                                                     d53GivenOverlayBranch,
		"a codebase in Review with an in-repo review env and an overlay also registered":                                                                d53GivenInRepo,
		`the "review" review environment was launched from the web UI`:                                                                                  d53LaunchUp("review"),
		`I launch the "review" review environment from the web UI`:                                                                                      d53Launch("review"),
		`I launch the "broken" review environment from the web UI`:                                                                                      d53Launch("broken"),
		`I tear the "review" review environment down from the web UI`:                                                                                   d53WhenTeardown,
		"the review env is up with the overlay branch's URL on the panel":                                                                               d53ThenUpWithBranchURL,
		"its scripts saw DRAIVER_CODEBASE_DIR as the codebase review worktree and DRAIVER_OVERLAY_DIR as an overlay checkout at the overlay branch tip": d53ThenContract,
		"the launched env uses the overlay branch's healthcheck and URL, not overlay main's":                                                            d53ThenBranchNotMain,
		"down and the confirm-down healthcheck reran with the same overlay env":                                                                         d53ThenTeardownReplayed,
		"the env is confirmed down and both review worktrees are removed":                                                                               d53ThenDownAndClean,
		"the launch failed, down ran with the overlay env, and no review worktree is left":                                                              d53ThenFailedUpClean,
		"the in-repo review env is up and its scripts saw no overlay env vars":                                                                          d53ThenInRepoNoVars,
	} {
		if _, clash := registry[text]; clash {
			panic("acceptance: duplicate step registration: " + text)
		}
		registry[text] = fn
	}
}

// d53World is the scenario fixture: an isolated cache (so the webui's review worktrees
// and any managed worktree land in a temp dir), config, data root, a codebase repo with
// the attempt branch, an overlay repo, and the live webui.
type d53World struct {
	b         *board
	root      store.Root
	mgr       *reviewenv.Manager
	cacheDir  string
	proof     string
	repo      string
	overlay   string
	branchTip string // overlay branch tip (draiver/<ticket>/<attempt>)
	mainTip   string // overlay main tip
	rec       reviewenv.Record
	upRec     reviewenv.Record // the Up record a teardown started from
}

func d53Get(w *World) (*d53World, error) {
	d, _ := w.get("d53").(*d53World)
	if d == nil {
		return nil, errors.New("no drv-053 review-env fixture in scope")
	}
	return d, nil
}

// d53Env sets an env var for the scenario, restoring the old value on cleanup.
func d53Env(w *World, k, v string) {
	prev, had := os.LookupEnv(k)
	os.Setenv(k, v)
	w.defer_(func() {
		if had {
			os.Setenv(k, prev)
		} else {
			os.Unsetenv(k)
		}
	})
}

// overlay scripts: each records the env it saw, keyed by the instance, so a Then step
// can prove what up / the healthcheck / down received. up also marks the env live; the
// healthcheck probes that marker; down clears it. "$1" is the healthcheck's name, so
// the proof shows WHICH definition (branch vs main) ran.
var d53OverlayScripts = map[string]string{
	"review-env/record.sh": `#!/bin/sh
# record.sh <phase>: append the overlay contract this script saw to the proof dir.
{
  echo "phase=$1"
  echo "DRAIVER_CODEBASE_DIR=$DRAIVER_CODEBASE_DIR"
  echo "DRAIVER_OVERLAY_DIR=$DRAIVER_OVERLAY_DIR"
  echo "PWD=$PWD"
  echo "OVERLAY_HEAD=$(git -C "$DRAIVER_OVERLAY_DIR" rev-parse HEAD 2>/dev/null)"
  echo "---"
} >> "$DRV053_PROOF/$DRAIVER_REVIEW_INSTANCE.log"
`,
	"review-env/up.sh": `sh "$DRAIVER_OVERLAY_DIR/review-env/record.sh" up
touch "$DRV053_PROOF/$DRAIVER_REVIEW_INSTANCE.live"
echo "up: overlay review env $DRAIVER_REVIEW_INSTANCE started"
`,
	"review-env/check.sh": `sh "$DRAIVER_OVERLAY_DIR/review-env/record.sh" "healthcheck:$1"
test -f "$DRV053_PROOF/$DRAIVER_REVIEW_INSTANCE.live"
`,
	"review-env/down.sh": `sh "$DRAIVER_OVERLAY_DIR/review-env/record.sh" down
rm -f "$DRV053_PROOF/$DRAIVER_REVIEW_INSTANCE.live"
echo "down: overlay review env $DRAIVER_REVIEW_INSTANCE stopped"
`,
}

// d53OverlayPyramid declares two review-ready envs whose every command lives in the
// overlay, reached only via $DRAIVER_OVERLAY_DIR: "review" (healthcheck hc, URL path
// urlPath) and "broken", whose up starts the env and then fails — the half-started
// case a failed launch must still run down for.
func d53OverlayPyramid(hc, urlPath string) string {
	return `environments:
  - name: review
    up: sh "$DRAIVER_OVERLAY_DIR/review-env/up.sh"
    down: sh "$DRAIVER_OVERLAY_DIR/review-env/down.sh"
    healthchecks:
      - name: ` + hc + `
        script: sh "$DRAIVER_OVERLAY_DIR/review-env/check.sh" ` + hc + `
    url: http://127.0.0.1:${DRAIVER_REVIEW_PORT}/` + urlPath + `
  - name: broken
    up: 'sh "$DRAIVER_OVERLAY_DIR/review-env/up.sh" && echo "up: then the bring-up errors" >&2 && exit 7'
    down: sh "$DRAIVER_OVERLAY_DIR/review-env/down.sh"
    healthchecks:
      - name: ` + hc + `
        script: sh "$DRAIVER_OVERLAY_DIR/review-env/check.sh" ` + hc + `
    url: http://127.0.0.1:${DRAIVER_REVIEW_PORT}/broken
levels:
  - name: unit
    run: "true"
`
}

// d53InRepoPyramid is an in-repo review env whose scripts record whether ANY overlay
// var leaked into them (they print "unset" when absent).
const d53InRepoPyramid = `environments:
  - name: review
    up: 'printf "phase=up\nDRAIVER_CODEBASE_DIR=%s\nDRAIVER_OVERLAY_DIR=%s\nPWD=%s\n---\n" "${DRAIVER_CODEBASE_DIR-unset}" "${DRAIVER_OVERLAY_DIR-unset}" "$PWD" >> "$DRV053_PROOF/$DRAIVER_REVIEW_INSTANCE.log" && touch "$DRV053_PROOF/$DRAIVER_REVIEW_INSTANCE.live"'
    down: 'rm -f "$DRV053_PROOF/$DRAIVER_REVIEW_INSTANCE.live"'
    healthchecks:
      - name: live
        script: 'printf "phase=healthcheck\nDRAIVER_OVERLAY_DIR=%s\n---\n" "${DRAIVER_OVERLAY_DIR-unset}" >> "$DRV053_PROOF/$DRAIVER_REVIEW_INSTANCE.log" && test -f "$DRV053_PROOF/$DRAIVER_REVIEW_INSTANCE.live"'
    url: http://127.0.0.1:${DRAIVER_REVIEW_PORT}/in-repo
levels:
  - name: unit
    run: "true"
`

func d53WriteFiles(dir string, files map[string]string) error {
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func d53GitInit(dir string) error {
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if err := gitc(dir, args...); err != nil {
			return err
		}
	}
	return nil
}

// d53Seed stands up the shared fixture. inRepoPyramid, when non-empty, is committed on
// the codebase attempt branch (the regression case); empty leaves the codebase
// pyramid-less so the overlay supplies it. An overlay is registered either way.
func d53Seed(w *World, inRepoPyramid string) (*d53World, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, errors.New("git not on PATH")
	}
	d := &d53World{}
	d.cacheDir = mustTemp(w, "d53-cache-")
	d53Env(w, "XDG_CACHE_HOME", d.cacheDir)
	d.proof = mustTemp(w, "d53-proof-")
	d53Env(w, d53Proof, d.proof)

	// The codebase: main + the attempt branch carrying the reviewed change.
	d.repo = mustTemp(w, "d53-repo-")
	if err := d53GitInit(d.repo); err != nil {
		return nil, err
	}
	if err := d53WriteFiles(d.repo, map[string]string{"README.md": "codebase\n"}); err != nil {
		return nil, err
	}
	branch := "draiver/" + d53Ticket + "/" + d53Att
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}, {"checkout", "-q", "-b", branch}} {
		if err := gitc(d.repo, args...); err != nil {
			return nil, err
		}
	}
	files := map[string]string{"feature.txt": "the change under review\n"}
	if inRepoPyramid != "" {
		files[".test-pyramid.yaml"] = inRepoPyramid
	}
	if err := d53WriteFiles(d.repo, files); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "feature"}, {"checkout", "-q", "main"}} {
		if err := gitc(d.repo, args...); err != nil {
			return nil, err
		}
	}

	// The overlay: main carries the pyramid + scripts.
	d.overlay = mustTemp(w, "d53-overlay-")
	if err := d53GitInit(d.overlay); err != nil {
		return nil, err
	}
	ofiles := map[string]string{".test-pyramid.yaml": d53OverlayPyramid("main-ready", "from-main")}
	for k, v := range d53OverlayScripts {
		ofiles[k] = v
	}
	if err := d53WriteFiles(d.overlay, ofiles); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "overlay pyramid"}} {
		if err := gitc(d.overlay, args...); err != nil {
			return nil, err
		}
	}
	tip, err := worktree.HeadSHA(context.Background(), d.overlay)
	if err != nil {
		return nil, err
	}
	d.mainTip = tip

	// Data root: a Review attempt pointing at the codebase.
	d.root = store.Root{Dir: mustTemp(w, "d53-data-")}
	if err := d.root.EnsureAttemptDirs(d53Ticket, d53Att); err != nil {
		return nil, err
	}
	spec := "---\nid: " + d53Ticket + "\ntitle: drv-053 overlay review env\n---\n\n# drv-053 overlay review env\n"
	if err := os.WriteFile(d.root.SpecPath(d53Ticket), []byte(spec), 0o644); err != nil {
		return nil, err
	}
	meta := "---\nid: " + d53Att + "\nticket: " + d53Ticket + "\nrepo: " + d.repo + "\nbase: main\n---\n\n"
	if err := os.WriteFile(d.root.AttemptMetaPath(d53Ticket, d53Att), []byte(meta), 0o644); err != nil {
		return nil, err
	}
	for _, ev := range []event.Event{
		{Type: "created", Actor: "agent:acceptance", Body: "start"},
		{Type: "review", Actor: "agent:acceptance", Body: "claiming review"},
	} {
		if _, err := ticketlog.Append(d.root, d53Ticket, d53Att, ev); err != nil {
			return nil, err
		}
	}

	// Config: the overlay registered for the codebase.
	cfgPath := filepath.Join(mustTemp(w, "d53-cfg-"), "config.json")
	d53Env(w, "DRAIVER_CONFIG", cfgPath)
	if _, err := config.SetRepoSettings(cfgPath, d.repo, config.RepoSettingsUpdate{Overlay: &d.overlay}); err != nil {
		return nil, err
	}

	srv, err := web.New(d.root)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	d.b = &board{root: d.root, wd: d.repo, srv: ts}
	d.mgr = reviewenv.New(reviewenv.Options{Root: d.root})
	w.set("board", d.b)
	w.set("d53", d)
	// Safety net: whatever a scenario left Up/leaked is torn down so no marker or
	// worktree outlives it (the temp dirs themselves are removed by mustTemp).
	w.defer_(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		a, err := loadD53Attempt(d)
		if err != nil {
			return
		}
		for _, env := range []string{"review", "broken"} {
			d.mgr.Teardown(ctx, a, env, "scenario cleanup")
		}
	})
	return d, nil
}

func d53GivenOverlayCodebase(w *World, sr *StepRun) error {
	_, err := d53Seed(w, "")
	return err
}

// d53GivenOverlayBranch commits, ONLY on the attempt's overlay branch, a changed review
// env (healthcheck "branch-ready", URL path /from-branch), then leaves uncommitted WIP
// on overlay main — neither main's committed definition nor its WIP may reach review.
func d53GivenOverlayBranch(w *World, sr *StepRun) error {
	d, err := d53Get(w)
	if err != nil {
		return err
	}
	branch := "draiver/" + d53Ticket + "/" + d53Att
	if err := gitc(d.overlay, "checkout", "-q", "-b", branch); err != nil {
		return err
	}
	if err := d53WriteFiles(d.overlay, map[string]string{".test-pyramid.yaml": d53OverlayPyramid("branch-ready", "from-branch")}); err != nil {
		return err
	}
	for _, args := range [][]string{{"commit", "-q", "-am", "overlay branch: new healthcheck + URL"}, {"checkout", "-q", "main"}} {
		if err := gitc(d.overlay, args...); err != nil {
			return err
		}
	}
	tip, err := worktree.NewManager(d.overlay)
	if err != nil {
		return err
	}
	sha, ok, err := tip.BranchSHA(context.Background(), worktree.Key{Ticket: d53Ticket, Attempt: d53Att})
	if err != nil || !ok {
		return fmt.Errorf("overlay branch tip: ok=%v err=%v", ok, err)
	}
	d.branchTip = sha
	// Uncommitted WIP on overlay main: a definition that, if read, would launch a URL
	// pointing at /from-wip.
	return d53WriteFiles(d.overlay, map[string]string{".test-pyramid.yaml": d53OverlayPyramid("wip-ready", "from-wip")})
}

func d53GivenInRepo(w *World, sr *StepRun) error {
	_, err := d53Seed(w, d53InRepoPyramid)
	return err
}

func loadD53Attempt(d *d53World) (project.Attempt, error) {
	return project.LoadAttempt(d.root, d53Ticket, d53Att)
}

// d53Post POSTs to a review-env route on the live webui as the panel's button would.
func d53Post(d *d53World, env, verb string) (string, error) {
	u := d.b.url("/ticket/" + d53Ticket + "/" + d53Att + "/review-env/" + url.PathEscape(env) + "/" + verb)
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(""))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return string(body), fmt.Errorf("POST %s: status %d: %s", verb, resp.StatusCode, body)
	}
	return string(body), nil
}

// d53LaunchAndWait clicks Up on the web UI, then waits for the async launch goroutine
// to settle (the record leaves "starting").
func d53LaunchAndWait(w *World, env string) error {
	d, err := d53Get(w)
	if err != nil {
		return err
	}
	if _, err := d53Post(d, env, "up"); err != nil {
		return err
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		rec, ok, err := d.mgr.Load(d53Ticket, d53Att, env)
		if err != nil {
			return err
		}
		if ok && rec.State != reviewenv.Starting {
			d.rec = rec
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("review env %q never left starting", env)
}

func d53Launch(env string) StepFunc {
	return func(w *World, sr *StepRun) error { return d53LaunchAndWait(w, env) }
}

func d53LaunchUp(env string) StepFunc {
	return func(w *World, sr *StepRun) error {
		if err := d53LaunchAndWait(w, env); err != nil {
			return err
		}
		d, _ := d53Get(w)
		if d.rec.State != reviewenv.Up {
			return fmt.Errorf("launch state = %q, want up (message: %s)", d.rec.State, d.rec.Message)
		}
		d.upRec = d.rec
		return nil
	}
}

func d53WhenTeardown(w *World, sr *StepRun) error {
	d, err := d53Get(w)
	if err != nil {
		return err
	}
	if _, err := d53Post(d, "review", "down"); err != nil {
		return err
	}
	rec, _, err := d.mgr.Load(d53Ticket, d53Att, "review")
	if err != nil {
		return err
	}
	d.rec = rec
	return nil
}

// d53Proof reads the per-instance proof log the scripts appended to.
func d53ReadProof(d *d53World, instance string) string {
	data, _ := os.ReadFile(filepath.Join(d.proof, instance+".log"))
	return string(data)
}

// d53Phases splits the proof log into its per-invocation blocks, as key→value maps.
func d53Phases(log string) []map[string]string {
	var out []map[string]string
	for _, blk := range strings.Split(log, "---\n") {
		if strings.TrimSpace(blk) == "" {
			continue
		}
		m := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(blk), "\n") {
			if i := strings.IndexByte(line, '='); i >= 0 {
				m[line[:i]] = line[i+1:]
			}
		}
		out = append(out, m)
	}
	return out
}

func d53ThenUpWithBranchURL(w *World, sr *StepRun) error {
	d, err := d53Get(w)
	if err != nil {
		return err
	}
	if d.rec.State != reviewenv.Up {
		return fmt.Errorf("state = %q, want up (message: %s)", d.rec.State, d.rec.Message)
	}
	if !strings.HasSuffix(d.rec.URL, "/from-branch") {
		return fmt.Errorf("url = %q, want the overlay branch's /from-branch", d.rec.URL)
	}
	resp, err := http.Get(d.b.url("/ticket/" + d53Ticket + "/" + d53Att))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `data-testid="review-env-url"`) || !strings.Contains(string(body), d.rec.URL) {
		return fmt.Errorf("the attempt page does not reveal the Up URL %q", d.rec.URL)
	}
	return shoot(w, sr, "/ticket/"+d53Ticket+"/"+d53Att, "drv-053-overlay-review-env-up", true,
		`[data-testid="attempt-tab-review-env"]`, `[data-testid="attempt-tab-review-env"]`)
}

func d53ThenContract(w *World, sr *StepRun) error {
	d, err := d53Get(w)
	if err != nil {
		return err
	}
	rec := d.rec
	if rec.Worktree == "" || rec.OverlayWorktree == "" {
		return fmt.Errorf("record names no codebase/overlay review worktree: %+v", rec)
	}
	if rec.OverlayRepo != d.overlay || rec.OverlayCommit != d.branchTip {
		return fmt.Errorf("record overlay = %s@%s, want %s@%s (the overlay branch tip)", rec.OverlayRepo, rec.OverlayCommit, d.overlay, d.branchTip)
	}
	log := d53ReadProof(d, rec.Instance)
	phases := d53Phases(log)
	if len(phases) < 2 {
		return fmt.Errorf("want up + healthcheck proofs, got:\n%s", log)
	}
	for _, p := range phases {
		if p["DRAIVER_CODEBASE_DIR"] != rec.Worktree {
			return fmt.Errorf("%s saw DRAIVER_CODEBASE_DIR=%q, want the codebase review worktree %q", p["phase"], p["DRAIVER_CODEBASE_DIR"], rec.Worktree)
		}
		if p["PWD"] != rec.Worktree {
			return fmt.Errorf("%s ran in %q, want cwd = the codebase review worktree %q", p["phase"], p["PWD"], rec.Worktree)
		}
		if p["DRAIVER_OVERLAY_DIR"] != rec.OverlayWorktree {
			return fmt.Errorf("%s saw DRAIVER_OVERLAY_DIR=%q, want the overlay review checkout %q", p["phase"], p["DRAIVER_OVERLAY_DIR"], rec.OverlayWorktree)
		}
		if p["OVERLAY_HEAD"] != d.branchTip {
			return fmt.Errorf("%s's overlay checkout is at %q, want the overlay branch tip %q", p["phase"], p["OVERLAY_HEAD"], d.branchTip)
		}
	}
	if rec.OverlayWorktree == d.overlay {
		return errors.New("DRAIVER_OVERLAY_DIR is the overlay repo's own working copy, not a dedicated checkout")
	}
	attachText(sr, "drv-053-overlay-contract",
		"review-env record:\n  worktree         = "+rec.Worktree+
			"\n  overlay_repo     = "+rec.OverlayRepo+
			"\n  overlay_commit   = "+rec.OverlayCommit+" (overlay branch tip; overlay main is "+d.mainTip+")"+
			"\n  overlay_worktree = "+rec.OverlayWorktree+
			"\n  script_env       = "+strings.Join(rec.ScriptEnv, " ")+
			"\n\nwhat the overlay scripts saw:\n"+log)
	return nil
}

func d53ThenBranchNotMain(w *World, sr *StepRun) error {
	d, err := d53Get(w)
	if err != nil {
		return err
	}
	rec := d.rec
	if rec.State != reviewenv.Up {
		return fmt.Errorf("state = %q, want up (message: %s)", rec.State, rec.Message)
	}
	if len(rec.Healthchecks) != 1 || rec.Healthchecks[0].Name != "branch-ready" {
		return fmt.Errorf("launched healthchecks %+v, want the overlay branch's branch-ready", rec.Healthchecks)
	}
	if !strings.HasSuffix(rec.URL, "/from-branch") {
		return fmt.Errorf("url = %q, want /from-branch (not overlay main's /from-main or its WIP /from-wip)", rec.URL)
	}
	log := d53ReadProof(d, rec.Instance)
	if !strings.Contains(log, "phase=healthcheck:branch-ready") || strings.Contains(log, "main-ready") || strings.Contains(log, "wip-ready") {
		return fmt.Errorf("the healthcheck that ran was not the overlay branch's:\n%s", log)
	}
	attachText(sr, "drv-053-branch-not-main",
		"overlay main tip   "+d.mainTip+" (healthcheck main-ready, url /from-main; WIP: wip-ready, /from-wip)\n"+
			"overlay branch tip "+d.branchTip+" (healthcheck branch-ready, url /from-branch)\n\n"+
			"launched: commit="+rec.OverlayCommit+" healthcheck="+rec.Healthchecks[0].Name+" url="+rec.URL+"\n\n"+log)
	return nil
}

func d53ThenTeardownReplayed(w *World, sr *StepRun) error {
	d, err := d53Get(w)
	if err != nil {
		return err
	}
	up := d.upRec
	log := d53ReadProof(d, up.Instance)
	phases := d53Phases(log)
	var sawDown, sawConfirm bool
	downIdx := -1
	for i, p := range phases {
		if p["phase"] == "down" {
			sawDown, downIdx = true, i
		}
	}
	for i, p := range phases {
		if i > downIdx && downIdx >= 0 && strings.HasPrefix(p["phase"], "healthcheck:") {
			sawConfirm = true
		}
	}
	if !sawDown || !sawConfirm {
		return fmt.Errorf("teardown did not run down then the confirm-down healthcheck:\n%s", log)
	}
	for _, p := range phases[downIdx:] {
		if p["DRAIVER_CODEBASE_DIR"] != up.Worktree || p["DRAIVER_OVERLAY_DIR"] != up.OverlayWorktree {
			return fmt.Errorf("%s ran with a different overlay env than bring-up: %v", p["phase"], p)
		}
	}
	attachText(sr, "drv-053-teardown-replay", "launched with:\n  "+strings.Join(up.ScriptEnv, "\n  ")+"\n\nproof log:\n"+log)
	return nil
}

// d53WorktreeListed reports whether path appears in repo's `git worktree list`.
func d53WorktreeListed(repo, path string) (bool, error) {
	out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return false, err
	}
	return strings.Contains(string(out), "worktree "+path+"\n"), nil
}

func d53AssertGone(d *d53World, codebaseWT, overlayWT string) error {
	for _, c := range []struct{ repo, path string }{{d.repo, codebaseWT}, {d.overlay, overlayWT}} {
		if c.path == "" {
			return errors.New("the launch recorded no worktree path to check")
		}
		if _, err := os.Stat(c.path); !os.IsNotExist(err) {
			return fmt.Errorf("review worktree %s still exists", c.path)
		}
		if listed, err := d53WorktreeListed(c.repo, c.path); err != nil || listed {
			return fmt.Errorf("review worktree %s still registered with %s (err=%v)", c.path, c.repo, err)
		}
	}
	return nil
}

func d53ThenDownAndClean(w *World, sr *StepRun) error {
	d, err := d53Get(w)
	if err != nil {
		return err
	}
	if d.rec.State != reviewenv.Down {
		return fmt.Errorf("teardown state = %q, want down (message: %s)", d.rec.State, d.rec.Message)
	}
	if d.rec.Worktree != "" || d.rec.OverlayWorktree != "" {
		return fmt.Errorf("a confirmed-down record still names worktrees: %+v", d.rec)
	}
	if err := d53AssertGone(d, d.upRec.Worktree, d.upRec.OverlayWorktree); err != nil {
		return err
	}
	attachText(sr, "drv-053-teardown-clean", "state: "+string(d.rec.State)+"\nmessage: "+d.rec.Message+
		"\nremoved: "+d.upRec.Worktree+"\nremoved: "+d.upRec.OverlayWorktree)
	return nil
}

func d53ThenFailedUpClean(w *World, sr *StepRun) error {
	d, err := d53Get(w)
	if err != nil {
		return err
	}
	rec := d.rec
	if rec.State != reviewenv.Failed || !strings.Contains(rec.Message, "up failed") {
		return fmt.Errorf("state = %q message = %q, want a failed up", rec.State, rec.Message)
	}
	log := d53ReadProof(d, rec.Instance)
	phases := d53Phases(log)
	var down map[string]string
	for _, p := range phases {
		if p["phase"] == "down" {
			down = p
		}
	}
	if down == nil {
		return fmt.Errorf("a failed up did not run down:\n%s", log)
	}
	if down["DRAIVER_OVERLAY_DIR"] == "" || down["DRAIVER_CODEBASE_DIR"] == "" {
		return fmt.Errorf("down ran without the overlay env: %v", down)
	}
	if _, err := os.Stat(filepath.Join(d.proof, rec.Instance+".live")); !os.IsNotExist(err) {
		return errors.New("the half-started env is still live after the failed launch")
	}
	if err := d53AssertGone(d, down["DRAIVER_CODEBASE_DIR"], down["DRAIVER_OVERLAY_DIR"]); err != nil {
		return err
	}
	attachText(sr, "drv-053-failed-up", "state: "+string(rec.State)+"\nmessage: "+rec.Message+"\n\nproof log:\n"+log)
	return nil
}

func d53ThenInRepoNoVars(w *World, sr *StepRun) error {
	d, err := d53Get(w)
	if err != nil {
		return err
	}
	rec := d.rec
	if rec.State != reviewenv.Up || !strings.HasSuffix(rec.URL, "/in-repo") {
		return fmt.Errorf("state = %q url = %q, want the in-repo env up (message: %s)", rec.State, rec.URL, rec.Message)
	}
	if rec.OverlayRepo != "" || rec.OverlayWorktree != "" || rec.OverlayCommit != "" {
		return fmt.Errorf("in-repo launch recorded an overlay: %+v", rec)
	}
	for _, kv := range rec.ScriptEnv {
		if strings.HasPrefix(kv, overlay.EnvCodebaseDir+"=") || strings.HasPrefix(kv, overlay.EnvOverlayDir+"=") {
			return fmt.Errorf("in-repo ScriptEnv carries an overlay var: %v", rec.ScriptEnv)
		}
	}
	log := d53ReadProof(d, rec.Instance)
	phases := d53Phases(log)
	if len(phases) < 2 {
		return fmt.Errorf("want up + healthcheck proofs, got:\n%s", log)
	}
	for _, p := range phases {
		if p["DRAIVER_OVERLAY_DIR"] != "unset" {
			return fmt.Errorf("%s saw DRAIVER_OVERLAY_DIR=%q, want it unset", p["phase"], p["DRAIVER_OVERLAY_DIR"])
		}
		if v, ok := p["DRAIVER_CODEBASE_DIR"]; ok && v != "unset" {
			return fmt.Errorf("%s saw DRAIVER_CODEBASE_DIR=%q, want it unset", p["phase"], v)
		}
		if v, ok := p["PWD"]; ok && v != rec.Worktree {
			return fmt.Errorf("%s ran in %q, want the review worktree %q", p["phase"], v, rec.Worktree)
		}
	}
	attachText(sr, "drv-053-in-repo-regression", "script_env = "+strings.Join(rec.ScriptEnv, " ")+"\n\nproof log:\n"+log)
	return nil
}
