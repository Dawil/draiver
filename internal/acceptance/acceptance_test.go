package acceptance

import (
	"path/filepath"
	"runtime"
	"testing"
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
