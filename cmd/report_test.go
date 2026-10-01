package cmd

import (
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/report"
	"github.com/Dawil/draiver/internal/store"
)

// TestReportFromJSONIsSelfContained renders an explicit cucumber-JSON to a file and
// asserts it is a genuine standalone report: the screenshot is inlined as a base64
// data: URI (checked after HTML-entity unescaping, per the attribute-escaping
// gotcha) and nothing is referenced from the by-reference asset route.
func TestReportFromJSONIsSelfContained(t *testing.T) {
	dir := newTicket(t)

	jsonPath := filepath.Join(t.TempDir(), "run.json")
	if err := os.WriteFile(jsonPath, report.ExampleJSON(), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(t.TempDir(), "report.html")

	if _, code := run(t, "--data", dir, "report", "PROJ-1", "--json", jsonPath, "-o", outPath); code != 0 {
		t.Fatalf("report --json exited %d", code)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	out := html.UnescapeString(string(raw))
	if !strings.Contains(out, "data:image/png;base64,") {
		t.Error("standalone report does not inline the screenshot as a data: URI")
	}
	// The by-reference variant points <img src> at the asset route (…/report/asset?run=…);
	// a standalone file must have no such reference. (Decoded text embeddings may mention
	// the route in prose, so match the URL signature, not a bare path.)
	if strings.Contains(out, "report/asset?") {
		t.Error("standalone report references the by-reference asset route; it must inline everything")
	}
	if !strings.Contains(out, "Standalone cucumber HTML report (drv-018)") {
		t.Error("report is missing the feature name from the cucumber-JSON")
	}
}

// TestReportDemoSeedsDiscoverableRun asserts --demo writes a run the webui's own
// discovery layer finds under the drv-017 contract path, and still renders a file.
func TestReportDemoSeedsDiscoverableRun(t *testing.T) {
	dir := newTicket(t)
	outPath := filepath.Join(t.TempDir(), "report.html")

	outStr, code := run(t, "--data", dir, "report", "PROJ-1", "--demo", "-o", outPath)
	if code != 0 {
		t.Fatalf("report --demo exited %d: %s", code, outStr)
	}

	runs, err := report.DiscoverRuns(store.Root{Dir: dir}, "PROJ-1", "0001")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("want 1 seeded run discoverable by the webui, got %d", len(runs))
	}
	if runs[0].CucumberJSON == "" {
		t.Error("seeded run has no cucumber-JSON for the report route to load")
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("report file not written: %v", err)
	}
}

// TestReportNoRunErrors confirms the command fails cleanly when there is nothing to
// render — no captured run, no --json, no --demo.
func TestReportNoRunErrors(t *testing.T) {
	dir := newTicket(t)
	_, code, err := runE(t, "--data", dir, "report", "PROJ-1")
	if code == 0 {
		t.Fatal("expected a nonzero exit when no run is available")
	}
	if err == nil || !strings.Contains(err.Error(), "no BDD run") {
		t.Errorf("want a 'no BDD run' error, got %v", err)
	}
}
