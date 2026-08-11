package agent

import (
	"encoding/json"
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestTotals_AddSumsPerRequestFrames(t *testing.T) {
	var tot Totals
	tot.Add(Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 0, CacheCreationTokens: 50, ContextTokens: 150})
	tot.Add(Usage{InputTokens: 5, OutputTokens: 30, CacheReadTokens: 145, CacheCreationTokens: 0, ContextTokens: 150})

	if tot.InputTokens != 105 || tot.OutputTokens != 50 || tot.CacheReadTokens != 145 || tot.CacheCreationTokens != 50 {
		t.Fatalf("totals = %+v, want summed input=105 output=50 read=145 creation=50", tot)
	}
}

func TestTotals_DerivedMetrics(t *testing.T) {
	tot := Totals{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 50, CacheCreationTokens: 30}

	if !tot.CachingActive() {
		t.Fatal("caching_active should be true when a cache field is non-zero")
	}
	// hit ratio = read / (read + creation + input) = 50 / 180
	if got := tot.CacheHitRatio(); !approx(got, 50.0/180.0) {
		t.Fatalf("hit ratio = %v, want %v", got, 50.0/180.0)
	}
	// normalized work = input + creation + read + output, all 1x
	if got := tot.NormalizedWork(); got != 200 {
		t.Fatalf("normalized work = %d, want 200", got)
	}
	// billed input = input + 1.25*creation + 0.1*read = 100 + 37.5 + 5
	if got := tot.BilledInputTokens(); !approx(got, 142.5) {
		t.Fatalf("billed input = %v, want 142.5", got)
	}
}

// caching_active catches the below-min-prefix / silent-invalidator miss: cost
// climbs (input tokens flow) but not a single cache read or write ever lands.
func TestTotals_CachingActiveFalseWithoutCacheFields(t *testing.T) {
	tot := Totals{InputTokens: 5000, OutputTokens: 400}
	if tot.CachingActive() {
		t.Fatal("caching_active should be false when both cache fields are zero")
	}
	if got := tot.CacheHitRatio(); got != 0 {
		t.Fatalf("hit ratio = %v, want 0", got)
	}
}

func TestTotals_ZeroValueDoesNotDivideByZero(t *testing.T) {
	var tot Totals
	if got := tot.CacheHitRatio(); got != 0 {
		t.Fatalf("empty hit ratio = %v, want 0", got)
	}
	if tot.CachingActive() {
		t.Fatal("empty totals must not be caching_active")
	}
}

// meter.json / attempt.md carry the raw fields AND the derived metrics: marshal
// emits both; unmarshal restores only the raw state, and re-deriving matches.
func TestTotals_MarshalCarriesDerivedUnmarshalReadsRaw(t *testing.T) {
	tot := Totals{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 50, CacheCreationTokens: 30}

	b, err := json.Marshal(tot)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
		"cache_hit_ratio", "normalized_work", "billed_input_tokens", "caching_active",
	} {
		if _, ok := m[k]; !ok {
			t.Fatalf("marshaled totals missing derived key %q: %s", k, b)
		}
	}
	if m["caching_active"] != true {
		t.Fatalf("caching_active = %v, want true", m["caching_active"])
	}

	// Round-trip: unmarshal reads only the raw fields, and re-deriving matches.
	var back Totals
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back != tot {
		t.Fatalf("round-trip totals = %+v, want %+v", back, tot)
	}
}

// A hand-edited derived value in the file can never desync: unmarshal ignores it
// and the next marshal recomputes it from the raw fields.
func TestTotals_UnmarshalIgnoresStaleDerived(t *testing.T) {
	in := []byte(`{"input_tokens":100,"cache_read_tokens":50,"cache_creation_tokens":30,"cache_hit_ratio":0.99,"caching_active":false}`)
	var tot Totals
	if err := json.Unmarshal(in, &tot); err != nil {
		t.Fatal(err)
	}
	if !tot.CachingActive() {
		t.Fatal("caching_active must be recomputed from raw fields, not read from the file")
	}
	if approx(tot.CacheHitRatio(), 0.99) {
		t.Fatal("hit ratio must be recomputed from raw fields, not read from the file")
	}
}

// A reuse-heavy attempt (a warm shared prefix read back far more than it was
// written) has a reuse factor well above 1 and a positive cost-avoided figure —
// the ">100%" win the bounded hit ratio can never show.
func TestTotals_ReuseHeavyCostAvoided(t *testing.T) {
	tot := Totals{InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 8000, CacheCreationTokens: 1000}

	ratio, ok := tot.ReadCreationRatio()
	if !ok || !approx(ratio, 8.0) {
		t.Fatalf("reuse factor = %v (ok=%v), want 8.0", ratio, ok)
	}
	// baseline = 10,000; billed = 1000 + 1.25*1000 + 0.1*8000 = 3,050.
	// avoided = 10,000 − 3,050 = 6,950 = 0.9*8000 − 0.25*1000.
	if got := tot.CostAvoidedInputTokens(); !approx(got, 6950) {
		t.Fatalf("cost avoided = %v, want 6950", got)
	}
	if got := tot.CostAvoidedInputTokens(); got <= 0 {
		t.Fatalf("reuse-heavy cost avoided must be positive, got %v", got)
	}
	// pct = 6,950 / 10,000 = 0.695.
	if got := tot.PctCostAvoided(); !approx(got, 0.695) {
		t.Fatalf("pct cost avoided = %v, want 0.695", got)
	}
	// The pointer field is populated on the derived view.
	m := tot.Metrics()
	if m.ReadCreationRatio == nil || !approx(*m.ReadCreationRatio, 8.0) {
		t.Fatalf("Metrics.ReadCreationRatio = %v, want 8.0", m.ReadCreationRatio)
	}
	if !approx(m.CostAvoidedInputTokens, 6950) || !approx(m.PctCostAvoided, 0.695) {
		t.Fatalf("Metrics cost fields = (%v, %v), want (6950, 0.695)", m.CostAvoidedInputTokens, m.PctCostAvoided)
	}
}

// A write-only attempt (cache written, never read — a silent invalidator) pays
// the 1.25x write premium for nothing: cost avoided goes NEGATIVE and must not be
// clamped, because that sign is the whole signal. The reuse factor is still
// defined (C > 0) and is 0 (no reads).
func TestTotals_WriteOnlyNegativeSavings(t *testing.T) {
	tot := Totals{InputTokens: 500, OutputTokens: 100, CacheReadTokens: 0, CacheCreationTokens: 2000}

	ratio, ok := tot.ReadCreationRatio()
	if !ok || ratio != 0 {
		t.Fatalf("write-only reuse factor = %v (ok=%v), want 0 with ok=true", ratio, ok)
	}
	// avoided = 0.9*0 − 0.25*2000 = −500.
	if got := tot.CostAvoidedInputTokens(); !approx(got, -500) {
		t.Fatalf("cost avoided = %v, want -500", got)
	}
	// baseline = 500 + 0 + 2000 = 2,500; pct = −500/2500 = −0.2.
	if got := tot.PctCostAvoided(); !approx(got, -0.2) {
		t.Fatalf("pct cost avoided = %v, want -0.2", got)
	}
}

// When nothing was written (C == 0) the reuse factor is undefined: no
// divide-by-zero, ok is false, and the derived Metrics carries a nil pointer
// (serialized as null / rendered "—"). Cost avoided is still well-defined — reads
// against a prefix written in an earlier session are pure savings (0.9*R).
func TestTotals_NoCreationRatioNull(t *testing.T) {
	tot := Totals{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 400, CacheCreationTokens: 0}

	if ratio, ok := tot.ReadCreationRatio(); ok || ratio != 0 {
		t.Fatalf("ratio with C==0 = %v (ok=%v), want 0 with ok=false", ratio, ok)
	}
	m := tot.Metrics()
	if m.ReadCreationRatio != nil {
		t.Fatalf("Metrics.ReadCreationRatio = %v, want nil when C==0", *m.ReadCreationRatio)
	}
	// avoided = 0.9*400 − 0.25*0 = 360; baseline = 500; pct = 0.72.
	if got := tot.CostAvoidedInputTokens(); !approx(got, 360) {
		t.Fatalf("cost avoided = %v, want 360", got)
	}
	if got := tot.PctCostAvoided(); !approx(got, 0.72) {
		t.Fatalf("pct cost avoided = %v, want 0.72", got)
	}

	// The nil pointer marshals to JSON null, and PctCostAvoided is present.
	b, err := json.Marshal(tot)
	if err != nil {
		t.Fatal(err)
	}
	var mp map[string]any
	if err := json.Unmarshal(b, &mp); err != nil {
		t.Fatal(err)
	}
	if v, ok := mp["read_creation_ratio"]; !ok || v != nil {
		t.Fatalf("read_creation_ratio = %v (present=%v), want JSON null", v, ok)
	}
	for _, k := range []string{"cost_avoided_input_tokens", "pct_cost_avoided"} {
		if _, ok := mp[k]; !ok {
			t.Fatalf("marshaled metrics missing derived key %q: %s", k, b)
		}
	}
}

// The zero-value totals must not divide by zero on any of the new derived fields.
func TestTotals_ZeroValueNewMetricsSafe(t *testing.T) {
	var tot Totals
	if _, ok := tot.ReadCreationRatio(); ok {
		t.Fatal("empty reuse factor must be undefined (ok=false)")
	}
	if got := tot.CostAvoidedInputTokens(); got != 0 {
		t.Fatalf("empty cost avoided = %v, want 0", got)
	}
	if got := tot.PctCostAvoided(); got != 0 {
		t.Fatalf("empty pct cost avoided = %v, want 0", got)
	}
}
