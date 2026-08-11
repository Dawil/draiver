package agent

import "encoding/json"

// Cache-token price multipliers relative to the base input-token rate, per
// Anthropic prompt-caching pricing: a cache read bills at ~0.1x input, a cache
// write at 1.25x for the 5-minute TTL and 2x for the 1-hour TTL. These are the
// pricing foundation for token-based budget accounting — metering cached tokens
// at full input rate over-reports whenever caching is active. The adapter reports
// a single cache_creation figure with no TTL breakdown, so writes are priced at
// the 5m default (CacheWrite5mMultiplier); a future TTL-aware path can apply the
// 1h multiplier.
const (
	CacheReadMultiplier    = 0.1
	CacheWrite5mMultiplier = 1.25
	CacheWrite1hMultiplier = 2.0
)

// Totals is a session-cumulative token tally, summed across every per-request
// usage frame in a session — the durable prompt-caching telemetry foundation.
// It is distinct from Usage, which is the latest single per-request snapshot the
// live context gauge reads; Totals is the running sum folded into meter.json and,
// on retire, into attempt.md. Only the raw summed fields are state; the derived
// metrics (Metrics) are recomputed from them on demand, so they can never drift.
type Totals struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

// Add folds one per-request usage snapshot into the running totals. Callers pass
// only real per-request frames (ContextTokens > 0); the cumulative turn-end frame
// must never be added, since its usage already sums the turn's requests.
func (t *Totals) Add(u Usage) {
	t.InputTokens += u.InputTokens
	t.OutputTokens += u.OutputTokens
	t.CacheReadTokens += u.CacheReadTokens
	t.CacheCreationTokens += u.CacheCreationTokens
}

// CachingActive reports whether prompt caching engaged at all this session: true
// iff at least one cache field is non-zero. It catches the failure modes a raw
// cost figure hides — a prefix below the cache minimum, or a silent invalidator —
// where the session pays full rate and never lands a single cache read or write.
func (t Totals) CachingActive() bool {
	return t.CacheReadTokens > 0 || t.CacheCreationTokens > 0
}

// CacheHitRatio is cache_read / (cache_read + cache_creation + input): the share
// of prompt tokens served from cache. Zero when no prompt tokens were seen.
func (t Totals) CacheHitRatio() float64 {
	denom := t.CacheReadTokens + t.CacheCreationTokens + t.InputTokens
	if denom == 0 {
		return 0
	}
	return float64(t.CacheReadTokens) / float64(denom)
}

// ReadCreationRatio is cache_read / cache_creation: the honest reuse factor —
// how many tokens were served from cache per token written to it. Unlike
// CacheHitRatio (a token-weighted average bounded 0..1), this exceeds 1 whenever a
// written prefix is read back more than once, so it can surface the ">100%" reuse
// the average dilutes away. Undefined when nothing was written (C == 0): ok is
// false so the display shows "—" rather than an infinity.
func (t Totals) ReadCreationRatio() (ratio float64, ok bool) {
	if t.CacheCreationTokens == 0 {
		return 0, false
	}
	return float64(t.CacheReadTokens) / float64(t.CacheCreationTokens), true
}

// CostAvoidedInputTokens is the input-token-equivalent saved versus the no-cache
// baseline: every prompt token billed at the full input rate. Caching changes the
// per-token rate, not the token volume, so the baseline is I+R+C at 1x and the
// saving is baseline − BilledInputTokens(), which simplifies to 0.9·R − 0.25·C
// (reads bill at 0.1x, writes at 1.25x). It is deliberately NOT clamped: a
// write-only session (cache written, never read) yields a negative figure, the
// signal of a silent invalidator paying the write premium for nothing.
func (t Totals) CostAvoidedInputTokens() float64 {
	baseline := float64(t.InputTokens + t.CacheReadTokens + t.CacheCreationTokens)
	return baseline - t.BilledInputTokens()
}

// PctCostAvoided is CostAvoidedInputTokens as a share of the no-cache baseline
// (I+R+C) — the bounded, intuitive "share of prompt spend the cache avoided". Zero
// when there were no prompt tokens to bill. It can go negative for a write-only
// session, matching CostAvoidedInputTokens.
func (t Totals) PctCostAvoided() float64 {
	baseline := t.InputTokens + t.CacheReadTokens + t.CacheCreationTokens
	if baseline == 0 {
		return 0
	}
	return t.CostAvoidedInputTokens() / float64(baseline)
}

// NormalizedWork is input + cache_creation + cache_read + output, every token at
// 1x — a caching-agnostic measure of the raw work a session did, so two runs that
// cache differently can be compared on equal footing (A/B).
func (t Totals) NormalizedWork() int {
	return t.InputTokens + t.CacheCreationTokens + t.CacheReadTokens + t.OutputTokens
}

// BilledInputTokens is the input-rate-equivalent prompt-token count after applying
// the cache multipliers: input at 1x, cache writes at 1.25x (5m TTL), cache reads
// at 0.1x. Output tokens price at the separate output rate and are excluded. This
// is the figure token-based cost/budget accounting must meter instead of summing
// raw prompt tokens at full rate, which over-reports whenever caching is active.
func (t Totals) BilledInputTokens() float64 {
	return float64(t.InputTokens) +
		CacheWrite5mMultiplier*float64(t.CacheCreationTokens) +
		CacheReadMultiplier*float64(t.CacheReadTokens)
}

// Metrics is the serialization-facing view of Totals: the raw summed fields plus
// the derived caching telemetry, carried verbatim in meter.json and attempt.md so
// downstream tools (A/B compares, budget accounting) read the numbers without
// recomputing. Build it with Totals.Metrics().
type Metrics struct {
	InputTokens         int     `json:"input_tokens" yaml:"input_tokens"`
	OutputTokens        int     `json:"output_tokens" yaml:"output_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens" yaml:"cache_read_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens" yaml:"cache_creation_tokens"`
	CacheHitRatio       float64 `json:"cache_hit_ratio" yaml:"cache_hit_ratio"`
	NormalizedWork      int     `json:"normalized_work" yaml:"normalized_work"`
	BilledInputTokens   float64 `json:"billed_input_tokens" yaml:"billed_input_tokens"`
	CachingActive       bool    `json:"caching_active" yaml:"caching_active"`
	// ReadCreationRatio is the reuse factor R/C; nil (rendered null / "—") when
	// nothing was written, so a divide-by-zero never leaks an Inf into the file.
	ReadCreationRatio *float64 `json:"read_creation_ratio" yaml:"read_creation_ratio"`
	// CostAvoidedInputTokens is the input-token-equivalent the cache saved vs. the
	// no-cache baseline; PctCostAvoided is that as a share of the baseline. Both may
	// be negative for a write-only session (kept honest, not clamped).
	CostAvoidedInputTokens float64 `json:"cost_avoided_input_tokens" yaml:"cost_avoided_input_tokens"`
	PctCostAvoided         float64 `json:"pct_cost_avoided" yaml:"pct_cost_avoided"`
}

// Metrics computes the derived view from the raw totals.
func (t Totals) Metrics() Metrics {
	m := Metrics{
		InputTokens:            t.InputTokens,
		OutputTokens:           t.OutputTokens,
		CacheReadTokens:        t.CacheReadTokens,
		CacheCreationTokens:    t.CacheCreationTokens,
		CacheHitRatio:          t.CacheHitRatio(),
		NormalizedWork:         t.NormalizedWork(),
		BilledInputTokens:      t.BilledInputTokens(),
		CachingActive:          t.CachingActive(),
		CostAvoidedInputTokens: t.CostAvoidedInputTokens(),
		PctCostAvoided:         t.PctCostAvoided(),
	}
	if r, ok := t.ReadCreationRatio(); ok {
		m.ReadCreationRatio = &r
	}
	return m
}

// MarshalJSON emits the full derived Metrics view, so meter.json carries the raw
// fields, the derived metrics, and caching_active together.
func (t Totals) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.Metrics())
}

// UnmarshalJSON reads back only the raw summed fields — the derived metrics are
// recomputed on the next marshal, so a hand-edited derived value can never
// desync the totals it is supposed to describe.
func (t *Totals) UnmarshalJSON(b []byte) error {
	var aux struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheReadTokens     int `json:"cache_read_tokens"`
		CacheCreationTokens int `json:"cache_creation_tokens"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	t.InputTokens = aux.InputTokens
	t.OutputTokens = aux.OutputTokens
	t.CacheReadTokens = aux.CacheReadTokens
	t.CacheCreationTokens = aux.CacheCreationTokens
	return nil
}
