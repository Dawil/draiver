package acceptance

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/bddexec"
	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/report"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/worktree"
)

// steps_singlerung_drv047.go realises drv-047 over genuine code paths: the board Rerun
// is now overlay-aware (bddexec routes through overlay.ResolvePyramid, exactly as
// `draiver test` does), `draiver test --only RUNG` runs and captures one rung on demand
// without advancing the gate, and a captured report is viewable in any state, not only
// Review. The overlay-rerun and report-outside-review scenarios drive a live board (so
// the gate and the rendered page are the real ones); the `--only` scenarios shell the
// freshly-built binary (the behaviour under test — exit code, capture, no gate event —
// lives in cmd/ and is only observable by running it), reusing steps_overlay.go's
// build-once overlayBin and the isolated data-root/config/cache discipline.
func init() {
	steps := map[string]StepFunc{
		// features/single-rung-reports/overlay-rerun.feature
		"an overlay-only codebase whose overlay pyramid has a bound BDD rung":                drv047GivenOverlayOnlyBoard,
		"the board shows the Rerun button for the overlay-only attempt":                      drv047ThenRerunButtonShown,
		"I rerun the acceptance rung from the board for the overlay-only codebase":           drv047WhenOverlayRerun,
		"the overlay rung ran with cwd the codebase and both overlay env vars exported":      drv047ThenOverlayEnvExported,
		"a fresh artefact set is captured for the overlay-only attempt":                      drv047ThenOverlayCaptured,

		// features/single-rung-reports/test-only.feature
		"a codebase whose required unit rung fails and whose acceptance rung is green": drv047GivenOnlyCodebase,
		"I run draiver test acceptance --only for the attempt":                         drv047WhenTestOnly,
		"the acceptance rung ran green and exits zero":                                 drv047ThenOnlyGreen,
		"the acceptance report is captured into the attempt artefact store":            drv047ThenOnlyCaptured,
		"no test-result gate event is recorded":                                        drv047ThenNoGateEvent,
		"I run draiver test acceptance --only --log for the attempt":                   drv047WhenTestOnlyLog,
		"the run is refused and explains a single-rung run cannot record a gate result": drv047ThenOnlyLogRefused,

		// features/single-rung-reports/report-outside-review.feature
		"a Running attempt (not in Review) with a captured BDD run": drv047GivenRunningWithRun,
		"the attempt page shows its BDD report tab":                 drv047ThenPageShowsBddTab,
		"the report route renders the captured report":              drv047ThenReportRenders,
	}
	for text, fn := range steps {
		registry[text] = fn
	}
}

// ---- (a) overlay-aware board Rerun -----------------------------------------------

// drv047OverlayProofEnv names the env var the overlay rung writes its exported
// DRAIVER_CODEBASE_DIR / DRAIVER_OVERLAY_DIR into. It is set in os.Environ() by the Given
// step and inherited by bddexec's runShell (which layers extraEnv over os.Environ()), so
// the proof survives the rerun worktree's teardown — the overlay-authored run itself
// records the execution contract it observed.
const drv047OverlayProofEnv = "DRV047_OVERLAY_PROOF"

// drv047OverlayPyramid is the overlay repo's pyramid: a bound BDD acceptance rung whose
// run records the two exported env vars into the proof file and copies the overlay's
// seed report into the codebase cwd (where capture reads cucumber_json, relative to the
// codebase worktree). The healthcheck is a genuine always-green precondition.
const drv047OverlayPyramid = `environments:
  - name: bdd
    healthchecks:
      - name: ready
        script: "true"
levels:
  - name: acceptance
    run: 'printf "%s|%s" "$DRAIVER_CODEBASE_DIR" "$DRAIVER_OVERLAY_DIR" > "$DRV047_OVERLAY_PROOF" && cp "$DRAIVER_OVERLAY_DIR/report.json" "$DRAIVER_CODEBASE_DIR/cucumber.json"'
    environment: bdd
    cucumber_json: cucumber.json
`

// drv047SeedOverlayBoard stands up an overlay-only codebase (NO in-repo pyramid) with a
// registered overlay carrying the bound BDD rung, plus a live board over a Running
// ACC-1/0001 attempt and a pre-existing captured run (so the report panel — and its
// Rerun button — render before the rerun). XDG_CACHE_HOME, DRAIVER_CONFIG and the proof
// env are redirected to scenario-scoped temp (restored on cleanup) so the worktrees the
// web layer and bddexec cut, and the config they read, stay under temp.
func drv047SeedOverlayBoard(w *World) (*board, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not on PATH")
	}
	ctx := context.Background()

	cache := mustTemp(w, "drv047-cache-")
	drv047SetEnv(w, "XDG_CACHE_HOME", cache)
	configPath := filepath.Join(mustTemp(w, "drv047-cfg-"), "config.json")
	drv047SetEnv(w, "DRAIVER_CONFIG", configPath)
	proof := filepath.Join(mustTemp(w, "drv047-proof-"), "proof")
	drv047SetEnv(w, drv047OverlayProofEnv, proof)

	// Codebase repo: a real repo with a committed base and NO .test-pyramid.yaml.
	repo := mustTemp(w, "drv047-repo-")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "base"},
	} {
		if err := gitc(repo, args...); err != nil {
			return nil, err
		}
	}
	// The attempt's managed worktree + branch, with a reviewed commit for Rerun to cut at.
	wm, err := worktree.NewManager(repo)
	if err != nil {
		return nil, err
	}
	key := worktree.Key{Ticket: accTicket, Attempt: accAtt}
	wt, err := wm.Create(ctx, worktree.Spec{Key: key})
	if err != nil {
		return nil, err
	}
	if err := gitcAcc(wt.Path, "commit", "-q", "--allow-empty", "-m", "attempt work"); err != nil {
		return nil, err
	}

	// Overlay repo: carries the bound BDD rung + the seed report its run copies in.
	overlayRepo := mustTemp(w, "drv047-overlay-")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if err := gitc(overlayRepo, args...); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(overlayRepo, ".test-pyramid.yaml"), []byte(drv047OverlayPyramid), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(overlayRepo, "report.json"), []byte(seedCucumberJSON), 0o644); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "overlay pyramid"}} {
		if err := gitc(overlayRepo, args...); err != nil {
			return nil, err
		}
	}
	// Register the overlay explicitly for the codebase, in the isolated config both the
	// gate (BoundBDDRung) and Rerun (resolveRerunPyramid) read via config.Load("").
	if _, err := config.SetRepoSettings(configPath, repo, config.RepoSettingsUpdate{Overlay: &overlayRepo}); err != nil {
		return nil, err
	}

	// Data root + a Running attempt pointing at the codebase repo, with a pre-existing run.
	dataDir := mustTemp(w, "drv047-data-")
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(accTicket, accAtt); err != nil {
		return nil, err
	}
	spec := "---\nid: " + accTicket + "\ntitle: drv-047 overlay rerun\n---\n\n# drv-047 overlay rerun\n"
	if err := os.WriteFile(root.SpecPath(accTicket), []byte(spec), 0o644); err != nil {
		return nil, err
	}
	meta := "---\nid: " + accAtt + "\nticket: " + accTicket + "\nrepo: " + repo + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(accTicket, accAtt), []byte(meta), 0o644); err != nil {
		return nil, err
	}
	if _, err := ticketlog.Append(root, accTicket, accAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return nil, err
	}
	b := &board{root: root, wd: repo, srv: nil}
	if err := seedRun(b, "20260101T090000Z"); err != nil {
		return nil, err
	}

	srv, err := web.New(root)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)
	b.srv = ts
	return b, nil
}

// drv047SetEnv sets an env var for the scenario and restores the prior value on cleanup,
// so the in-process config/cache redirection never leaks across scenarios (which run
// sequentially, so the swap is race-free).
func drv047SetEnv(w *World, key, val string) {
	prev, had := os.LookupEnv(key)
	os.Setenv(key, val)
	w.defer_(func() {
		if had {
			os.Setenv(key, prev)
		} else {
			os.Unsetenv(key)
		}
	})
}

func drv047GivenOverlayOnlyBoard(w *World, sr *StepRun) error {
	b, err := drv047SeedOverlayBoard(w)
	if err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

func drv047ThenRerunButtonShown(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	// The overlay-aware gate: it resolves the overlay in place (no worktree cut) and finds
	// the bound BDD rung, so the button lights for an overlay-only codebase.
	_, _, ok, err := bddexec.BoundBDDRung(b.wd)
	if err != nil {
		return fmt.Errorf("overlay-aware gate errored: %w", err)
	}
	if !ok {
		return fmt.Errorf("BoundBDDRung did not find the overlay's BDD rung for the overlay-only codebase")
	}
	// And the rendered attempt page carries the Rerun affordance.
	body, err := drv047Get(b, "/ticket/"+accTicket+"/"+accAtt)
	if err != nil {
		return err
	}
	if !strings.Contains(body, `data-testid="bdd-rerun"`) {
		return fmt.Errorf("attempt page does not show the Rerun button for the overlay-only attempt")
	}
	return nil
}

func drv047WhenOverlayRerun(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := bddexec.Rerun(ctx, b.root, b.wd, accTicket, accAtt)
	if err != nil {
		return fmt.Errorf("overlay rerun executor errored: %w", err)
	}
	w.set("outcome", out)
	return nil
}

func drv047ThenOverlayEnvExported(w *World, sr *StepRun) error {
	out, _ := w.get("outcome").(bddexec.Outcome)
	if out.Status != bddexec.StatusOK {
		return fmt.Errorf("overlay rerun status = %q, want ok (%v)", out.Status, out.Lines)
	}
	raw, err := os.ReadFile(os.Getenv(drv047OverlayProofEnv))
	if err != nil {
		return fmt.Errorf("overlay run wrote no proof — the env vars were not exported into the run: %w", err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(raw)), "|", 2)
	if len(parts) != 2 {
		return fmt.Errorf("overlay proof malformed: %q", string(raw))
	}
	codebaseDir, overlayDir := parts[0], parts[1]
	cache := os.Getenv("XDG_CACHE_HOME")
	if codebaseDir == "" || !strings.HasPrefix(codebaseDir, cache) || !strings.Contains(codebaseDir, "bdd-reruns") {
		return fmt.Errorf("DRAIVER_CODEBASE_DIR = %q, want the detached rerun codebase worktree under %q", codebaseDir, cache)
	}
	if overlayDir == "" || !strings.HasPrefix(overlayDir, cache) {
		return fmt.Errorf("DRAIVER_OVERLAY_DIR = %q, want a cut overlay worktree under %q", overlayDir, cache)
	}
	if overlayDir == codebaseDir {
		return fmt.Errorf("overlay and codebase dirs must differ, both = %q", codebaseDir)
	}
	attachText(sr, "overlay-rerun-env", "DRAIVER_CODEBASE_DIR="+codebaseDir+"\nDRAIVER_OVERLAY_DIR="+overlayDir)
	return nil
}

func drv047ThenOverlayCaptured(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	out, _ := w.get("outcome").(bddexec.Outcome)
	if out.RunKey == "" {
		return fmt.Errorf("overlay rerun captured no artefact run")
	}
	runs, err := report.DiscoverRuns(b.root, accTicket, accAtt)
	if err != nil {
		return err
	}
	// The seeded run plus the freshly captured one.
	if len(runs) < 2 {
		return fmt.Errorf("want at least 2 captured runs (seed + rerun), got %d", len(runs))
	}
	// Screenshot the refreshed board's BDD tab: the Rerun button + the regenerated report
	// for an overlay-only attempt.
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt, "overlay-rerun-report", true, `[data-testid="attempt-tab-bdd"]`, `[data-testid="attempt-tab-bdd"]`)
}

func drv047Get(b *board, path string) (string, error) {
	resp, err := http.Get(b.url(path))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s status = %d, want 200", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

// ---- (b)/(c) draiver test --only -------------------------------------------------

const (
	drv047OnlyTicket = "ONLY-1"
	drv047OnlyAtt    = "0001"
)

// drv047Only is the scenario-scoped fixture for the shelled `--only` runs: an isolated
// data root / config / cache and a codebase worktree whose in-repo pyramid has a RED
// required unit rung below a GREEN acceptance rung that writes a cucumber-JSON.
type drv047Only struct {
	dataRoot   string
	configPath string
	cacheDir   string
	checkout   string
	runOut     string
	runErr     error
}

// drv047OnlyPyramid: a required unit rung that always fails, and an acceptance BDD rung
// that passes and emits the seed cucumber-JSON. `--only acceptance` must run acceptance
// regardless of unit being red, and capture its report.
const drv047OnlyPyramid = `levels:
  - name: unit
    run: 'exit 1'
  - name: acceptance
    run: 'cp seed-cucumber.json cucumber.json'
    cucumber_json: cucumber.json
`

func drv047GivenOnlyCodebase(w *World, sr *StepRun) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git not on PATH")
	}
	ctx := context.Background()
	o := &drv047Only{}

	o.cacheDir = mustTemp(w, "only-cache-")
	drv047SetEnv(w, "XDG_CACHE_HOME", o.cacheDir)

	o.dataRoot = mustTemp(w, "only-data-")
	o.configPath = filepath.Join(mustTemp(w, "only-cfg-"), "config.json")

	repo := mustTemp(w, "only-repo-")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "base"},
	} {
		if err := gitc(repo, args...); err != nil {
			return err
		}
	}
	wm, err := worktree.NewManager(repo)
	if err != nil {
		return err
	}
	wt, err := wm.Create(ctx, worktree.Spec{Key: worktree.Key{Ticket: drv047OnlyTicket, Attempt: drv047OnlyAtt}})
	if err != nil {
		return err
	}
	o.checkout = wt.Path
	if err := os.WriteFile(filepath.Join(o.checkout, ".test-pyramid.yaml"), []byte(drv047OnlyPyramid), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(o.checkout, "seed-cucumber.json"), []byte(seedCucumberJSON), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "pyramid + seed"}} {
		if err := gitcAcc(o.checkout, args...); err != nil {
			return err
		}
	}

	root := store.Root{Dir: o.dataRoot}
	if err := root.EnsureAttemptDirs(drv047OnlyTicket, drv047OnlyAtt); err != nil {
		return err
	}
	spec := "---\nid: " + drv047OnlyTicket + "\ntitle: drv-047 only\n---\n\n# drv-047 only\n"
	if err := os.WriteFile(root.SpecPath(drv047OnlyTicket), []byte(spec), 0o644); err != nil {
		return err
	}
	meta := "---\nid: " + drv047OnlyAtt + "\nticket: " + drv047OnlyTicket + "\nrepo: " + repo + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(drv047OnlyTicket, drv047OnlyAtt), []byte(meta), 0o644); err != nil {
		return err
	}
	if _, err := ticketlog.Append(root, drv047OnlyTicket, drv047OnlyAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return err
	}
	w.set("only", o)
	return nil
}

func drv047OnlyGet(w *World) (*drv047Only, error) {
	o, _ := w.get("only").(*drv047Only)
	if o == nil {
		return nil, fmt.Errorf("no --only scenario set up in scope")
	}
	return o, nil
}

// drv047OnlyChildEnv replaces the four draiver knobs so the shelled binary targets the
// scenario's isolated data root / config / cache / actor, never the developer's own.
func drv047OnlyChildEnv(o *drv047Only) []string {
	drop := map[string]bool{
		"DRAIVER_DATA": true, "DRAIVER_CONFIG": true, "XDG_CACHE_HOME": true,
		"DRAIVER_ACTOR": true, "DRAIVER_ATTEMPT": true,
	}
	var env []string
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i >= 0 && drop[e[:i]] {
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"DRAIVER_DATA="+o.dataRoot,
		"DRAIVER_CONFIG="+o.configPath,
		"XDG_CACHE_HOME="+o.cacheDir,
		"DRAIVER_ACTOR=agent:acceptance",
		"DRAIVER_ATTEMPT="+drv047OnlyAtt,
	)
}

func drv047RunOnly(o *drv047Only, args ...string) (string, error) {
	bin, err := overlayBin()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = o.checkout
	cmd.Env = drv047OnlyChildEnv(o)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func drv047WhenTestOnly(w *World, sr *StepRun) error {
	o, err := drv047OnlyGet(w)
	if err != nil {
		return err
	}
	o.runOut, o.runErr = drv047RunOnly(o, "test", "acceptance", "--only")
	return nil
}

func drv047WhenTestOnlyLog(w *World, sr *StepRun) error {
	o, err := drv047OnlyGet(w)
	if err != nil {
		return err
	}
	o.runOut, o.runErr = drv047RunOnly(o, "test", "acceptance", "--only", "--log")
	return nil
}

func drv047ThenOnlyGreen(w *World, sr *StepRun) error {
	o, err := drv047OnlyGet(w)
	if err != nil {
		return err
	}
	if o.runErr != nil {
		return fmt.Errorf("`test acceptance --only` exited non-zero though acceptance is green: %v\n%s", o.runErr, o.runOut)
	}
	if !strings.Contains(o.runOut, "running only rung \"acceptance\"") {
		return fmt.Errorf("output does not note a single-rung run:\n%s", o.runOut)
	}
	attachText(sr, "test-only-output", o.runOut)
	return nil
}

func drv047ThenOnlyCaptured(w *World, sr *StepRun) error {
	o, err := drv047OnlyGet(w)
	if err != nil {
		return err
	}
	runs, err := report.DiscoverRuns(store.Root{Dir: o.dataRoot}, drv047OnlyTicket, drv047OnlyAtt)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return fmt.Errorf("`--only` captured no report into the attempt artefact store\n%s", o.runOut)
	}
	return nil
}

func drv047ThenNoGateEvent(w *World, sr *StepRun) error {
	o, err := drv047OnlyGet(w)
	if err != nil {
		return err
	}
	events, err := ticketlog.Read(store.Root{Dir: o.dataRoot}, drv047OnlyTicket, drv047OnlyAtt)
	if err != nil {
		return err
	}
	for _, ev := range events {
		if ev.Type == "test-result" {
			return fmt.Errorf("`--only` recorded a test-result gate event (#%d) — it must never advance the gate", ev.Seq)
		}
	}
	return nil
}

func drv047ThenOnlyLogRefused(w *World, sr *StepRun) error {
	o, err := drv047OnlyGet(w)
	if err != nil {
		return err
	}
	if o.runErr == nil {
		return fmt.Errorf("`--only --log` should be refused (non-zero exit) but succeeded:\n%s", o.runOut)
	}
	if !strings.Contains(o.runOut, "mutually exclusive") || !strings.Contains(o.runOut, "cannot record a gate result") {
		return fmt.Errorf("refusal does not clearly explain the single-rung/gate conflict:\n%s", o.runOut)
	}
	attachText(sr, "only-log-refused", o.runOut)
	return nil
}

// ---- (d) reports viewable outside Review -----------------------------------------

func drv047GivenRunningWithRun(w *World, sr *StepRun) error {
	b, err := seedBoard(w, greenPyramid)
	if err != nil {
		return err
	}
	if err := seedRun(b, "20260101T100000Z"); err != nil {
		return err
	}
	w.set("board", b)
	// Guard the premise: the attempt is Running, NOT Review.
	a, err := project.LoadAttempt(b.root, accTicket, accAtt)
	if err != nil {
		return err
	}
	if a.State != project.Running {
		return fmt.Errorf("fixture attempt is %q, want Running (not Review)", a.State)
	}
	return nil
}

func drv047ThenPageShowsBddTab(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	a, err := project.LoadAttempt(b.root, accTicket, accAtt)
	if err != nil {
		return err
	}
	if a.State == project.Review {
		return fmt.Errorf("attempt is in Review — the scenario must exercise an attempt outside Review")
	}
	body, err := drv047Get(b, "/ticket/"+accTicket+"/"+accAtt)
	if err != nil {
		return err
	}
	if !strings.Contains(body, `data-testid="attempt-tab-bdd"`) {
		return fmt.Errorf("a non-Review attempt with a captured run does not show its BDD report tab")
	}
	return nil
}

func drv047ThenReportRenders(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	body, err := drv047Get(b, "/ticket/"+accTicket+"/"+accAtt+"/report")
	if err != nil {
		return err
	}
	if !strings.Contains(body, "<html") {
		return fmt.Errorf("report route did not render an HTML report for the non-Review attempt")
	}
	// Screenshot the rendered report page served outside Review.
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt+"/report", "report-outside-review", true, "", "")
}
