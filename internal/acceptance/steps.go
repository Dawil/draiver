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
	"path/filepath"
	"strings"
	"time"

	"github.com/Dawil/draiver/internal/bddexec"
	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/report"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/web"
	"github.com/Dawil/draiver/internal/webshot"
)

// World is scenario-scoped state threaded through a scenario's steps. cleanup runs
// the deferred teardowns a step registered (temp worktrees / data roots / the live
// server), so the runner leaves no fixtures behind.
type World struct {
	bag      map[string]any
	cleanups []func()
}

func (w *World) set(k string, v any) { w.bag[k] = v }
func (w *World) get(k string) any    { return w.bag[k] }
func (w *World) defer_(fn func())    { w.cleanups = append(w.cleanups, fn) }
func (w *World) cleanup() {
	for i := len(w.cleanups) - 1; i >= 0; i-- {
		w.cleanups[i]()
	}
}

// StepRun collects what a step attaches: screenshot embeddings that become the
// scenario's artefacts in both the cucumber-JSON and the screenshots dir.
type StepRun struct {
	Embeddings []cucumber.Embedding
}

// StepFunc is a step definition: it acts on the World and records artefacts on the
// StepRun, returning an error to fail the step.
type StepFunc func(w *World, sr *StepRun) error

var errUndefined = errors.New("no step definition for this step")

// registry maps a step's text to its definition. "And"/"But" steps key on their own
// text, so the registry is keyword-agnostic. The shared "When I rerun…" step reads
// the World the preceding Given set up, which is how green and red rerun share one
// When.
var registry = map[string]StepFunc{
	// --- features/download.feature ---
	"a captured BDD run with an inlined screenshot": givenCapturedRun,
	"I render the standalone report for download":   whenRenderStandalone,
	"the report is a single self-contained HTML document": thenSelfContained,
	"the screenshot is inlined in the downloaded file":    thenScreenshotInlined,

	// --- features/rerun.feature ---
	"a bound BDD rung on a healthy environment":                  givenGreenRung,
	"a bound BDD rung whose environment healthcheck is red":      givenRedRung,
	"I rerun the acceptance rung from the board":                 whenRerun,
	"the rung runs green and a fresh artefact set is captured":   thenRerunGreen,
	"the rerun is blocked and no bogus report is captured":       thenRerunBlocked,
	"the block explains that the ball is passed back to the human": thenBlockExplains,
}

// invokeStep runs the step definition matching text, or reports errUndefined so the
// scenario fails loudly rather than a feature step silently going unexercised.
func invokeStep(w *World, text string, sr *StepRun) error {
	fn, ok := registry[text]
	if !ok {
		return errUndefined
	}
	return fn(w, sr)
}

// ---- the board fixture: a real webui over a seeded data root ----

const (
	accTicket = "ACC-1"
	accAtt    = "0001"
	accRung   = "acceptance"
	accEnv    = "bdd"
	accCommit = "nocommit" // the temp worktree is not a git repo → bddexec stamps "nocommit"
)

// onePxPNG is a 1x1 transparent PNG — the embedded image in a seeded run's
// cucumber-JSON, so the report the webui renders actually carries a picture. The
// scenario's own artefact is the Playwright screenshot of that rendered webui, not
// this placeholder.
const onePxPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

// board is a running webui server over a seeded attempt, plus the worktree its
// pyramid lives in — everything a scenario needs to drive a real drv-019 action and
// screenshot its page.
type board struct {
	root store.Root
	wd   string // worktree the attempt points at (carries .test-pyramid.yaml)
	srv  *httptest.Server
}

func (b *board) url(path string) string { return b.srv.URL + path }

// seedBoard builds a real, self-contained board: a temp worktree carrying
// pyramidYAML, a temp data root with a drv-019-shaped attempt pointing at it, and a
// live internal/web server over that root. All temp state and the server are
// registered for cleanup on the World.
func seedBoard(w *World, pyramidYAML string) (*board, error) {
	wd, err := os.MkdirTemp("", "acc-wd-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(wd) })
	if err := os.WriteFile(filepath.Join(wd, ".test-pyramid.yaml"), []byte(pyramidYAML), 0o644); err != nil {
		return nil, err
	}
	// The rung "produces" this cucumber.json; bddexec captures it on a rerun, so a
	// green rerun's refreshed report has real content (not an empty run).
	if err := os.WriteFile(filepath.Join(wd, "cucumber.json"), []byte(seedCucumberJSON), 0o644); err != nil {
		return nil, err
	}

	dataDir, err := os.MkdirTemp("", "acc-data-")
	if err != nil {
		return nil, err
	}
	w.defer_(func() { os.RemoveAll(dataDir) })
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs(accTicket, accAtt); err != nil {
		return nil, err
	}
	// Minimal on-disk attempt the board can load: spec (title), attempt.md (repo →
	// worktree, so CanRerun resolves true), and a created event.
	spec := "---\nid: " + accTicket + "\ntitle: drv-019 acceptance\n---\n\n# drv-019 acceptance\n"
	if err := os.WriteFile(root.SpecPath(accTicket), []byte(spec), 0o644); err != nil {
		return nil, err
	}
	meta := "---\nid: " + accAtt + "\nticket: " + accTicket + "\nrepo: " + wd + "\nbase: main\n---\n\n"
	if err := os.WriteFile(root.AttemptMetaPath(accTicket, accAtt), []byte(meta), 0o644); err != nil {
		return nil, err
	}
	if _, err := ticketlog.Append(root, accTicket, accAtt, event.Event{Type: "created", Actor: "agent:acceptance", Body: "start"}); err != nil {
		return nil, err
	}

	srv, err := web.New(root)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	w.defer_(ts.Close)

	return &board{root: root, wd: wd, srv: ts}, nil
}

// seedRun writes a captured BDD run under the attempt's artefact store so the
// board's report panel renders (HasRuns) before any rerun — mirroring drv-017's
// per-run key layout.
func seedRun(b *board, stamp string) error {
	dir := filepath.Join(b.root.ArtefactsDir(accTicket, accAtt), report.BDDArtefactSubdir, accRung, accEnv, accCommit, stamp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "cucumber.json"), []byte(seedCucumberJSON), 0o644)
}

var seedCucumberJSON = `[{"uri":"features/seed.feature","keyword":"Feature","name":"Seeded run","elements":[
  {"keyword":"Scenario","name":"A captured scenario","type":"scenario","steps":[
    {"keyword":"Then ","name":"a screenshot is captured","result":{"status":"passed","duration":1000000},
     "embeddings":[{"mime_type":"image/png","data":"` + onePxPNG + `","name":"screenshot"}]}]}]}]`

func currentBoard(w *World) (*board, error) {
	b, _ := w.get("board").(*board)
	if b == nil {
		return nil, errors.New("no board set up in scope")
	}
	return b, nil
}

// shoot captures a Playwright screenshot of a live webui page and attaches it as the
// step's artefact — the real webui image that replaced the old green/red squares
// (decision #22/#23).
func shoot(w *World, sr *StepRun, path, name string, full bool, waitSel string) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	png, err := webshot.Capture(ctx, b.url(path), webshot.Options{
		Width: 1280, Height: 900, FullPage: full, WaitSelector: waitSel,
	})
	if err != nil {
		return err
	}
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png",
		Name:     name,
		Data:     base64.StdEncoding.EncodeToString(png),
	})
	return nil
}

// ---- download steps: exercise the real download handler (drv-018 render, drv-019
// download) over the live webui, and screenshot the report page it serves ----

func givenCapturedRun(w *World, sr *StepRun) error {
	b, err := seedBoard(w, greenPyramid)
	if err != nil {
		return err
	}
	if err := seedRun(b, "20260101T100000Z"); err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

func whenRenderStandalone(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	// Exercise the real drv-019 download route end to end.
	resp, err := http.Get(b.url("/ticket/" + accTicket + "/" + accAtt + "/report/download"))
	if err != nil {
		return fmt.Errorf("GET report/download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("report/download status = %d, want 200", resp.StatusCode)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		return fmt.Errorf("download is not an attachment: Content-Disposition = %q", cd)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	w.set("download", string(body))
	return nil
}

func thenSelfContained(w *World, sr *StepRun) error {
	html, _ := w.get("download").(string)
	if !strings.Contains(html, "<html") {
		return errors.New("downloaded output is not an HTML document")
	}
	// Self-contained: CSS inlined (<style>), nothing linked out, no by-reference asset.
	if !strings.Contains(html, "<style") || strings.Contains(html, "<link ") {
		return errors.New("downloaded report is not self-contained (CSS not inlined)")
	}
	if strings.Contains(html, "/report/asset") {
		return errors.New("downloaded report references the asset route (not self-contained)")
	}
	// Screenshot the live report page the webui renders — the actual evidence surface.
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt+"/report", "report-page", true, "")
}

func thenScreenshotInlined(w *World, sr *StepRun) error {
	html, _ := w.get("download").(string)
	if !strings.Contains(html, "data:image/png;base64,") {
		return errors.New("downloaded report did not inline its screenshot as a data: URI")
	}
	// Screenshot the board attempt page carrying the report panel + download affordance.
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt, "board-download-affordance", true, `[data-testid="bdd-report"]`)
}

// ---- rerun steps: exercise the real in-process executor bddexec.Rerun (what the
// board's rerun button delegates to) and screenshot the board ----

const greenPyramid = `environments:
  - name: bdd
    healthchecks:
      - name: ready
        script: "true"
levels:
  - name: acceptance
    run: "true"
    environment: bdd
    cucumber_json: cucumber.json
`

const redPyramid = `environments:
  - name: bdd
    healthchecks:
      - name: ready
        script: "false"
levels:
  - name: acceptance
    run: "true"
    environment: bdd
    cucumber_json: cucumber.json
`

func givenGreenRung(w *World, sr *StepRun) error {
	b, err := seedBoard(w, greenPyramid)
	if err != nil {
		return err
	}
	// A pre-existing run so the panel renders the rerun's result side-by-side.
	if err := seedRun(b, "20260101T090000Z"); err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

func givenRedRung(w *World, sr *StepRun) error {
	b, err := seedBoard(w, redPyramid)
	if err != nil {
		return err
	}
	if err := seedRun(b, "20260101T090000Z"); err != nil {
		return err
	}
	w.set("board", b)
	return nil
}

func whenRerun(w *World, sr *StepRun) error {
	b, err := currentBoard(w)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := bddexec.Rerun(ctx, b.root, b.wd, accTicket, accAtt)
	if err != nil {
		return fmt.Errorf("rerun executor errored: %w", err)
	}
	w.set("outcome", out)
	return nil
}

func thenRerunGreen(w *World, sr *StepRun) error {
	out, _ := w.get("outcome").(bddexec.Outcome)
	if out.Status != bddexec.StatusOK {
		return fmt.Errorf("status = %q, want ok (%v)", out.Status, out.Lines)
	}
	if out.RunKey == "" {
		return errors.New("green rerun captured no artefact run")
	}
	// Screenshot the refreshed board: the panel now carries the regenerated run.
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt, "rerun-green-board", true, `[data-testid="bdd-report"]`)
}

func thenRerunBlocked(w *World, sr *StepRun) error {
	out, _ := w.get("outcome").(bddexec.Outcome)
	if out.Status != bddexec.StatusBlocked {
		return fmt.Errorf("status = %q, want blocked", out.Status)
	}
	if out.RunKey != "" {
		return fmt.Errorf("blocked rerun must capture nothing, got %q", out.RunKey)
	}
	// Screenshot the board: the rerun affordance is present — the surface a human
	// clicks and is told to pass the ball.
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt, "rerun-blocked-board", true, `[data-testid="bdd-rerun"]`)
}

func thenBlockExplains(w *World, sr *StepRun) error {
	out, _ := w.get("outcome").(bddexec.Outcome)
	joined := strings.Join(out.Lines, "\n")
	if !strings.Contains(joined, "blocked") || !strings.Contains(joined, "Pass the ball") {
		return fmt.Errorf("block banner does not pass the ball:\n%s", joined)
	}
	// Screenshot the rendered report page — the evidence that is NOT refreshed by a
	// block (the prior run still stands).
	return shoot(w, sr, "/ticket/"+accTicket+"/"+accAtt+"/report", "blocked-report-unchanged", true, "")
}
