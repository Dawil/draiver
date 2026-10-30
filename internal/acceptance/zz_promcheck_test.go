package acceptance

import (
	"testing"

	"github.com/Dawil/draiver/internal/webshot"
)

// TestDrv043PromotionsFeature runs the drv-043 promotions feature as a fast Go unit
// test. Several of its scenarios shoot real Playwright screenshots of the live webui,
// so — exactly like webshot's own TestCaptureProducesPNG — it SKIPS rather than fails
// when node/playwright/chromium is absent: the unit rung must stay green on a
// browserless box (and in a freshly-cut worktree where the `bdd` env's `npm install`
// has not run), while the bdd-bound `acceptance` rung is where a browser is a hard
// precondition and runs this feature in full.
func TestDrv043PromotionsFeature(t *testing.T) {
	if !webshot.Available() {
		t.Skip("webshot: node/playwright/chromium not available — the acceptance rung covers the UI scenarios")
	}
	res, err := Run("../../features/promotions", "/tmp/promshots")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.ScenariosFailed != 0 {
		t.Fatalf("failed=%d", res.ScenariosFailed)
	}
}
