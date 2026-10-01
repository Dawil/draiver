package report

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Dawil/draiver/internal/store"
)

// exampleCucumberJSON is a representative cucumber-JSON run that encodes drv-018's
// own acceptance criteria as BDD scenarios (with an embedded screenshot). It is a
// *demonstration* fixture: it lets a human generate and view a real report for the
// feature before drv-017's capture lands any genuine run. Being runner-agnostic
// standard cucumber-JSON, it also doubles as the example input the `draiver report`
// CLI renders.
//
//go:embed example.cucumber.json
var exampleCucumberJSON []byte

// ExampleJSON returns the embedded demonstration cucumber-JSON document. Callers
// may render it directly (Parse → Render) or seed it into an attempt's store with
// SeedDemoRun.
func ExampleJSON() []byte {
	out := make([]byte, len(exampleCucumberJSON))
	copy(out, exampleCucumberJSON)
	return out
}

// SeedDemoRun writes the embedded demonstration run into an attempt's artefact
// store under drv-017's per-run key contract
// (artefacts/bdd/<rung>/<env>/<commit>/<runstamp>/cucumber.json), so the already
// built webui embed and report routes light up and render it. It returns the run's
// Rel id (its stable webui/URL identifier).
//
// This is demonstration seeding only — it writes no execution/capture code and is
// superseded the moment drv-017's real capture writes a genuine run to the same
// path. runstamp must be a caller-supplied stamp (e.g. a UTC timestamp) so the run
// sorts and groups correctly; keeping it a parameter also keeps this function
// deterministic and testable.
func SeedDemoRun(root store.Root, id, attempt, commit, runstamp string) (string, error) {
	if commit == "" {
		commit = "demo0000"
	}
	if runstamp == "" {
		return "", fmt.Errorf("report: SeedDemoRun needs a non-empty runstamp")
	}
	rel := filepath.Join(BDDArtefactSubdir, "integration", "local", commit, runstamp)
	dir := filepath.Join(root.ArtefactsDir(id, attempt), rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("report: seed demo run dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, cucumberJSONNames[0]), exampleCucumberJSON, 0o644); err != nil {
		return "", fmt.Errorf("report: write demo cucumber-JSON: %w", err)
	}
	return filepath.ToSlash(rel), nil
}
