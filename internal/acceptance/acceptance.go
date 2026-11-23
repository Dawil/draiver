// Package acceptance is drv-019's own acceptance suite, authored as the executable
// realisation of the ticket's Gherkin criteria (docs/acceptance-criteria.md). It is
// a small, runner-agnostic BDD runner — not godog: it reads the repo's
// features/ (recursively, across the capability subfolders), runs a step registry
// whose steps exercise drv-019's real code
// (report.Render for the download, bddexec.Rerun for the guarded rerun), and emits
// standard cucumber-JSON plus PNG screenshot artefacts. draiver then consumes that
// JSON exactly as it would any BDD runner's (internal/cucumber, internal/report),
// so the acceptance rung, its capture, its report, and the board's rerun all run
// against genuine, reproducible evidence.
//
// Every scenario attaches at least one screenshot embedding on a step, satisfying
// "each scenario has a step with an artifact": the embeddings ride inside the
// cucumber-JSON (what the report renders) and are also written as files under the
// screenshots dir (what the rung's `artifacts:` captures side by side).
package acceptance

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dawil/draiver/internal/cucumber"
)

// outputs are the files the runner writes under outDir: the cucumber-JSON the rung
// declares as its report, and the screenshots dir the rung captures as artefacts.
const (
	cucumberJSONName = "cucumber.json"
	screenshotsDir   = "screenshots"
)

// Result is the outcome of a run: the emitted cucumber Report and how many
// scenarios failed, so a caller (the rung's main) can set its exit code.
type Result struct {
	Report          cucumber.Report
	ScenariosFailed int
	// ScenariosSelected / ScenariosTotal count the scenarios a focused run kept and
	// the scenarios discovered under featuresDir; equal for an unfocused run.
	ScenariosSelected int
	ScenariosTotal    int
}

// RunOptions focuses a run on a subset of the discovered scenarios (drv-063). The
// zero value runs everything.
type RunOptions struct {
	// Tags keeps a scenario carrying ANY of these tags (OR), counting the tags of
	// its feature and enclosing Rule as its own. A leading "@" is optional.
	Tags []string
	// Name keeps a scenario whose name contains this text (case-insensitive).
	Name string
}

// Focused reports whether the options select a subset rather than the whole suite.
func (o RunOptions) Focused() bool { return len(o.Tags) > 0 || strings.TrimSpace(o.Name) != "" }

// ParseTags splits a `--tags` value ("@drv-046,@ui" or "drv-046 ui") into tags.
func ParseTags(s string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		out = append(out, normTag(t))
	}
	return out
}

func normTag(t string) string {
	t = strings.TrimSpace(t)
	if !strings.HasPrefix(t, "@") {
		t = "@" + t
	}
	return t
}

// selects reports whether scenario gs of feature gf passes the focus filter.
func (o RunOptions) selects(gf GherkinFeature, gs GherkinScenario) bool {
	if n := strings.TrimSpace(o.Name); n != "" && !strings.Contains(strings.ToLower(gs.Name), strings.ToLower(n)) {
		return false
	}
	if len(o.Tags) == 0 {
		return true
	}
	have := map[string]bool{}
	for _, t := range gf.Tags {
		have[t] = true
	}
	if gs.Rule != nil {
		for _, t := range gs.Rule.Tags {
			have[t] = true
		}
	}
	for _, t := range gs.Tags {
		have[t] = true
	}
	for _, t := range o.Tags {
		if have[normTag(t)] {
			return true
		}
	}
	return false
}

// Run reads featuresDir, executes every scenario against the step registry, writes
// the cucumber-JSON and screenshot files under outDir, and returns the emitted
// report. A missing step definition fails its scenario (it is reported as a failed
// step), so the features and the registry cannot silently drift apart.
func Run(featuresDir, outDir string) (Result, error) {
	return RunFocused(featuresDir, outDir, RunOptions{})
}

// RunFocused is Run restricted to the scenarios opts selects (drv-063): features
// with no selected scenario are left out of the report entirely. Selecting nothing
// is an error, so a mistyped tag fails loudly instead of passing vacuously.
func RunFocused(featuresDir, outDir string, opts RunOptions) (Result, error) {
	features, err := ParseFeaturesDir(featuresDir)
	if err != nil {
		return Result{}, fmt.Errorf("parse features: %w", err)
	}
	if len(features) == 0 {
		return Result{}, fmt.Errorf("no *.feature files under %s", featuresDir)
	}
	total, selected := 0, 0
	var kept []GherkinFeature
	for _, gf := range features {
		total += len(gf.Scenarios)
		var scs []GherkinScenario
		for _, gs := range gf.Scenarios {
			if opts.selects(gf, gs) {
				scs = append(scs, gs)
			}
		}
		if len(scs) > 0 {
			gf.Scenarios = scs
			kept = append(kept, gf)
			selected += len(scs)
		}
	}
	if selected == 0 {
		return Result{}, fmt.Errorf("no scenario under %s matches the focus (tags %v, name %q) — of %d discovered", featuresDir, opts.Tags, opts.Name, total)
	}
	features = kept
	shotDir := filepath.Join(outDir, screenshotsDir)
	if err := os.MkdirAll(shotDir, 0o755); err != nil {
		return Result{}, err
	}

	var report cucumber.Report
	failed := 0
	for _, gf := range features {
		cf := cucumber.Feature{
			URI:         gf.URI,
			ID:          slug(gf.Name),
			Keyword:     "Feature",
			Name:        gf.Name,
			Description: gf.Description,
			Tags:        toTags(gf.Tags),
			Elements:    []cucumber.Element{},
		}
		for _, gs := range gf.Scenarios {
			el, ok := runScenario(gf, gs, shotDir)
			if !ok {
				failed++
			}
			cf.Elements = append(cf.Elements, el)
		}
		report = append(report, cf)
	}

	if err := writeJSON(filepath.Join(outDir, cucumberJSONName), report); err != nil {
		return Result{}, err
	}
	return Result{Report: report, ScenariosFailed: failed, ScenariosSelected: selected, ScenariosTotal: total}, nil
}

// MissingArtefacts lists "<feature>: <scenario>" for every PASSED scenario in r that
// attached no artefact on any step — the runtime half of the artefact rule, which
// the acceptance runner enforces on every run (the browser-free static half is
// TestEveryScenarioCanAttachAnArtefact). A failed scenario is excluded: its later
// steps were skipped, so a missing artefact there is a symptom, not the fault.
func MissingArtefacts(r cucumber.Report) []string {
	var out []string
	for _, f := range r {
		for _, el := range f.Elements {
			if el.Type == "background" {
				continue
			}
			passed, attached := true, false
			for _, st := range el.Steps {
				if st.Result.Status != "passed" {
					passed = false
				}
				if len(st.Embeddings) > 0 {
					attached = true
				}
			}
			if passed && !attached {
				out = append(out, f.Name+": "+el.Name)
			}
		}
	}
	return out
}

// runScenario executes one scenario's steps in order. The first failing or
// undefined step fails the scenario; every later step is recorded skipped (standard
// BDD short-circuiting). Embeddings a step attaches are both carried in the returned
// cucumber Element and written to shotDir as files. ok is false when the scenario
// did not pass.
func runScenario(gf GherkinFeature, gs GherkinScenario, shotDir string) (cucumber.Element, bool) {
	w := &World{bag: map[string]any{}}
	defer w.cleanup()

	el := cucumber.Element{
		ID:      slug(gf.Name) + ";" + slug(gs.Name),
		Keyword: "Scenario",
		Name:    gs.Name,
		Line:    gs.Line,
		Type:    "scenario",
		Tags:    toTags(gs.Tags),
		Rule:    toRule(gs.Rule),
		Steps:   []cucumber.Step{},
	}
	failed := false
	for i, gstep := range gs.Steps {
		st := cucumber.Step{Keyword: gstep.Keyword + " ", Name: gstep.Text, Line: gstep.Line}
		switch {
		case failed:
			st.Result = cucumber.Result{Status: "skipped"}
		default:
			sr := &StepRun{}
			err := invokeStep(w, gstep.Text, sr)
			if err != nil {
				failed = true
				if err == errUndefined {
					st.Result = cucumber.Result{Status: "undefined"}
				} else {
					st.Result = cucumber.Result{Status: "failed", ErrorMessage: err.Error()}
				}
			} else {
				st.Result = cucumber.Result{Status: "passed"}
			}
			st.Embeddings = persistEmbeddings(shotDir, gf.Name, gs.Name, i, sr.Embeddings)
		}
		el.Steps = append(el.Steps, st)
	}
	return el, !failed
}

// persistEmbeddings writes each embedding's decoded bytes to a file under shotDir
// (so the rung's `artifacts:` capture has real side-by-side files) and returns the
// embeddings unchanged for the cucumber-JSON. A write error is non-fatal: the
// embedding still rides in the JSON, which is the report's source of truth.
func persistEmbeddings(shotDir, feature, scenario string, step int, embs []cucumber.Embedding) []cucumber.Embedding {
	for i, e := range embs {
		raw, err := base64.StdEncoding.DecodeString(e.Data)
		if err != nil {
			continue
		}
		name := fmt.Sprintf("%s__%s__step%d__%d%s", slug(feature), slug(scenario), step, i, ext(e.MimeType))
		_ = os.WriteFile(filepath.Join(shotDir, name), raw, 0o644)
	}
	return embs
}

// toTags converts parsed `@tag` strings into cucumber Tags, so the emitted JSON
// carries the feature/scenario tags the drv-023 viewer facets and filters on.
func toTags(names []string) []cucumber.Tag {
	if len(names) == 0 {
		return nil
	}
	out := make([]cucumber.Tag, 0, len(names))
	for _, n := range names {
		out = append(out, cucumber.Tag{Name: n})
	}
	return out
}

// toRule converts a parsed Gherkin Rule into the per-element rule reference the
// cucumber-JSON carries, so the viewer groups consecutive same-rule scenarios into a
// `Rule:` sub-group. A scenario with no enclosing rule emits none.
func toRule(r *GherkinRule) *cucumber.Rule {
	if r == nil {
		return nil
	}
	return &cucumber.Rule{
		Keyword: "Rule",
		Name:    r.Name,
		Line:    r.Line,
		Tags:    toTags(r.Tags),
	}
}

func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WriteSummary prints a one-line human summary of a run to w, mirroring
// cucumber.Summary.String — what `draiver test` also prints when it consumes the
// report.
func WriteSummary(w io.Writer, r Result) {
	fmt.Fprintln(w, r.Report.Summary().String())
}

func ext(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png":
		return ".png"
	case "text/plain":
		return ".txt"
	default:
		return ".bin"
	}
}

// slug reduces a name to a stable [a-z0-9-] identifier for cucumber ids / filenames.
func slug(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
