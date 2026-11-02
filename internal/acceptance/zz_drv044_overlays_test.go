package acceptance

import "testing"

// TestDrv044OverlaysFeature runs the drv-044 overlays×promotions feature as a fast Go
// unit test. Unlike the drv-043 promotions test it does NOT skip when webshot is absent:
// its one UI scenario captures the screenshot only when a browser is available and
// attaches the rendered HTML otherwise (d44ThenPromotedAtHead), so every assertion — the
// `config repo --overlay` round-trip and the overlay-only promotion going green from the
// button — runs on a browserless box too. The bdd-bound `acceptance` rung runs the same
// feature with a browser present, where the Playwright screenshot is captured in full.
func TestDrv044OverlaysFeature(t *testing.T) {
	res, err := Run("../../features/overlays", t.TempDir())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.ScenariosFailed != 0 {
		t.Fatalf("failed=%d", res.ScenariosFailed)
	}
}
