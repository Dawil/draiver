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
