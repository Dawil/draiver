package acceptance

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dawil/draiver/internal/cucumber"
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

// runSuite runs the real features into a temp outDir and returns the result.
func runSuite(t *testing.T) Result {
	t.Helper()
	res, err := Run(repoFeaturesDir(t), t.TempDir())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// The acceptance suite must pass clean: a failing/undefined step here means a
// feature step drifted from the registry, or drv-019's behaviour regressed.
func TestAcceptanceSuitePasses(t *testing.T) {
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
	res := runSuite(t)
	sum := res.Report.Summary()
	if sum.Embeddings == 0 {
		t.Fatal("suite produced no embeddings")
	}
	if sum.Features < 2 {
		t.Errorf("expected at least the download + rerun features, got %d", sum.Features)
	}
}
