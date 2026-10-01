package acceptance

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dawil/draiver/internal/bddexec"
	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/report"
	"github.com/Dawil/draiver/internal/store"
)

// World is scenario-scoped state threaded through a scenario's steps. cleanup runs
// the deferred teardowns a step registered (temp worktrees / data roots), so the
// runner leaves no fixtures behind.
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

// StepRun collects what a step attaches: screenshot/text embeddings that become the
// scenario's artefacts in both the cucumber-JSON and the screenshots dir.
type StepRun struct {
	Embeddings []cucumber.Embedding
}

// attachShot adds a deterministic PNG screenshot embedding — drv-019's artefact per
// scenario. ok tints it green/red so the report reads at a glance.
func (sr *StepRun) attachShot(name string, ok bool) {
	sr.Embeddings = append(sr.Embeddings, cucumber.Embedding{
		MimeType: "image/png",
		Name:     name,
		Data:     base64.StdEncoding.EncodeToString(screenshotPNG(ok)),
	})
}

// StepFunc is a step definition: it acts on the World and records artefacts on the
// StepRun, returning an error to fail the step.
type StepFunc func(w *World, sr *StepRun) error

var errUndefined = errors.New("no step definition for this step")

// registry maps a step's text to its definition. "And"/"But" steps key on their own
// text, so the registry is keyword-agnostic. The same text maps to one func even
// across scenarios (e.g. the shared "When I rerun…" step reads the World the
// preceding Given set up), which is how green and red rerun share one When.
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

// ---- download steps: exercise report.Render (drv-018, served/downloaded by drv-019) ----

func givenCapturedRun(w *World, sr *StepRun) error {
	shot := base64.StdEncoding.EncodeToString(screenshotPNG(true))
	rep := &report.Report{Features: []report.Feature{{
		URI:     "features/demo.feature",
		Keyword: "Feature",
		Name:    "Demo",
		Elements: []report.Element{{
			Keyword: "Scenario",
			Name:    "A scenario with a screenshot",
			Type:    "scenario",
			Steps: []report.Step{{
				Keyword: "Then ",
				Name:    "a screenshot is captured",
				Result:  report.Result{Status: report.StatusPassed},
				Embeddings: []report.Embedding{{
					MimeType: "image/png",
					Name:     "screenshot",
					Data:     shot,
				}},
			}},
		}},
	}}}
	w.set("report", rep)
	w.set("shot", shot)
	return nil
}

func whenRenderStandalone(w *World, sr *StepRun) error {
	rep, _ := w.get("report").(*report.Report)
	if rep == nil {
		return errors.New("no fixture report in scope")
	}
	// Resolve nil → the fully self-contained document drv-019 serves as the download.
	html, err := report.Render(rep, report.Options{Title: "acceptance download"})
	if err != nil {
		return fmt.Errorf("render standalone report: %w", err)
	}
	w.set("html", string(html))
	return nil
}

func thenSelfContained(w *World, sr *StepRun) error {
	html, _ := w.get("html").(string)
	if !strings.Contains(html, "<html") {
		return errors.New("rendered output is not an HTML document")
	}
	// Self-contained means the CSS/JS are inlined, not linked — no external <link>.
	if !strings.Contains(html, "<style") || strings.Contains(html, "<link ") {
		return errors.New("report is not self-contained (CSS not inlined)")
	}
	sr.attachShot("standalone-report", true)
	return nil
}

func thenScreenshotInlined(w *World, sr *StepRun) error {
	html, _ := w.get("html").(string)
	shot, _ := w.get("shot").(string)
	if shot == "" || !strings.Contains(html, shot) {
		return errors.New("screenshot bytes are not inlined in the downloaded file")
	}
	sr.attachShot("inlined-screenshot", true)
	return nil
}

// ---- rerun steps: exercise bddexec.Rerun (drv-019 executor) over temp fixtures ----

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

func givenGreenRung(w *World, sr *StepRun) error { return seedRerunWorld(w, greenPyramid) }
func givenRedRung(w *World, sr *StepRun) error   { return seedRerunWorld(w, redPyramid) }

// seedRerunWorld builds a temp worktree (pyramid + a pre-placed cucumber.json the
// rung "produces") and a temp data root with a live attempt, mirroring bddexec's own
// test fixture — so whenRerun can drive the real executor.
func seedRerunWorld(w *World, pyramidYAML string) error {
	wd, err := os.MkdirTemp("", "acc-wd-")
	if err != nil {
		return err
	}
	w.defer_(func() { os.RemoveAll(wd) })
	if err := os.WriteFile(filepath.Join(wd, ".test-pyramid.yaml"), []byte(pyramidYAML), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(wd, "cucumber.json"), []byte(`[]`), 0o644); err != nil {
		return err
	}
	dataDir, err := os.MkdirTemp("", "acc-data-")
	if err != nil {
		return err
	}
	w.defer_(func() { os.RemoveAll(dataDir) })
	root := store.Root{Dir: dataDir}
	if err := root.EnsureAttemptDirs("ACC-1", "0001"); err != nil {
		return err
	}
	w.set("wd", wd)
	w.set("root", root)
	return nil
}

func whenRerun(w *World, sr *StepRun) error {
	wd, _ := w.get("wd").(string)
	root, _ := w.get("root").(store.Root)
	if wd == "" {
		return errors.New("no bound rung set up in scope")
	}
	out, err := bddexec.Rerun(context.Background(), root, wd, "ACC-1", "0001")
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
	sr.attachShot("rerun-green-"+out.RunKey, true)
	return nil
}

func thenRerunBlocked(w *World, sr *StepRun) error {
	out, _ := w.get("outcome").(bddexec.Outcome)
	if out.Status != bddexec.StatusBlocked {
		return fmt.Errorf("status = %q, want blocked", out.Status)
	}
	if out.RunKey != "" {
		return fmt.Errorf("blocked rerun must capture nothing, got %q", out.RunKey)
	}
	sr.attachShot("rerun-blocked", false)
	return nil
}

func thenBlockExplains(w *World, sr *StepRun) error {
	out, _ := w.get("outcome").(bddexec.Outcome)
	joined := strings.Join(out.Lines, "\n")
	if !strings.Contains(joined, "blocked") || !strings.Contains(joined, "Pass the ball") {
		return fmt.Errorf("block banner does not pass the ball:\n%s", joined)
	}
	sr.attachShot("pass-the-ball", false)
	return nil
}

// screenshotPNG renders a deterministic status banner PNG — a genuine, reproducible
// image artefact (no fonts, no randomness): green when ok, red otherwise, with a
// lighter inner panel so it reads as a captured frame rather than a flat swatch.
func screenshotPNG(ok bool) []byte {
	const w, h = 320, 90
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{0x16, 0x7a, 0x3b, 0xff}
	panel := color.RGBA{0x3d, 0xa5, 0x63, 0xff}
	if !ok {
		bg = color.RGBA{0xb0, 0x2a, 0x37, 0xff}
		panel = color.RGBA{0xd1, 0x5c, 0x66, 0xff}
	}
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(12, 12, w-12, h-12), &image.Uniform{panel}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
