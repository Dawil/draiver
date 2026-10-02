package acceptance

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dawil/draiver/internal/cucumber"
	"github.com/Dawil/draiver/internal/webshot"
)

// repoFeaturesDir resolves the repo's real features/ dir from this test file's
// location (internal/acceptance → ../../features), so the suite runs the actual
// Gherkin a human authored, not a fixture — the test and the shipped features
// cannot drift.
func repoFeaturesDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "features")
}

// requireBrowser skips execution-based tests when no browser is available. The
// steps now screenshot the live webui via Playwright (decision #23), so executing
// them needs node + playwright + chromium — the hard precondition of the bdd-bound
// acceptance rung. On a browserless box (e.g. the unit rung before `npm install`)
// these skip, and the drift check below still guards the feature/registry contract.
func requireBrowser(t *testing.T) {
	t.Helper()
	if !webshot.Available() {
		t.Skip("acceptance suite needs a playwright browser; it runs for real under the bdd-bound `acceptance` rung")
	}
}

// runSuite runs the real features into a temp outDir and returns the result.
func runSuite(t *testing.T) Result {
	t.Helper()
	res, err := Run(repoFeaturesDir(t), t.TempDir())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// TestFeaturesHaveStepDefinitions is the browser-free drift guard: every step in
// every shipped feature must resolve to a registry definition. It runs always (no
// browser needed), so a feature step that drifts from the registry fails fast even
// on the unit rung.
func TestFeaturesHaveStepDefinitions(t *testing.T) {
	features, err := ParseFeaturesDir(repoFeaturesDir(t))
	if err != nil {
		t.Fatalf("parse features: %v", err)
	}
	if len(features) < 2 {
		t.Fatalf("expected at least the download + rerun features, got %d", len(features))
	}
	for _, f := range features {
		for _, sc := range f.Scenarios {
			for _, st := range sc.Steps {
				if _, ok := registry[st.Text]; !ok {
					t.Errorf("feature %q scenario %q: no registry step for %q", f.Name, sc.Name, st.Text)
				}
			}
		}
	}
}

// The acceptance suite must pass clean: a failing/undefined step here means a
// feature step drifted from the registry, or drv-019's behaviour regressed.
func TestAcceptanceSuitePasses(t *testing.T) {
	requireBrowser(t)
	res := runSuite(t)
	if res.ScenariosFailed != 0 {
		t.Fatalf("%d scenario(s) failed:\n%s", res.ScenariosFailed, res.Report.Summary())
	}
	if !res.Report.Passed() {
		t.Fatalf("report is not all-green: %s", res.Report.Summary())
	}
}

// AC #4 / deliverable: every scenario in the report has at least one step carrying
// an artefact (embedding) — the property the board's report page relies on.
func TestEveryScenarioHasAnArtifact(t *testing.T) {
	requireBrowser(t)
	res := runSuite(t)
	sum := res.Report.Summary()
	if sum.Scenarios == 0 {
		t.Fatal("no scenarios ran")
	}
	for _, f := range res.Report {
		for _, el := range f.Elements {
			if el.Type == "background" {
				continue
			}
			if !elementHasEmbedding(el) {
				t.Errorf("scenario %q in feature %q has no step with an artefact", el.Name, f.Name)
			}
		}
	}
}

func elementHasEmbedding(el cucumber.Element) bool {
	for _, st := range el.Steps {
		if len(st.Embeddings) > 0 {
			return true
		}
	}
	return false
}

// The emitted JSON must be exactly what draiver consumes downstream (the rung's
// capture, the report renderer): a re-parse of the marshalled report round-trips to
// a valid, green cucumber document with the embeddings intact.
func TestEmittedJSONIsConsumableCucumber(t *testing.T) {
	requireBrowser(t)
	res := runSuite(t)
	sum := res.Report.Summary()
	if sum.Embeddings == 0 {
		t.Fatal("suite produced no embeddings")
	}
	if sum.Features < 2 {
		t.Errorf("expected at least the download + rerun features, got %d", sum.Features)
	}
}
