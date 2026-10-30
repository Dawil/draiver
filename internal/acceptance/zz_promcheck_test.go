package acceptance

import "testing"

func TestDrv043PromotionsFeature(t *testing.T) {
	res, err := Run("../../features/promotions", "/tmp/promshots")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.ScenariosFailed != 0 {
		t.Fatalf("failed=%d", res.ScenariosFailed)
	}
}
