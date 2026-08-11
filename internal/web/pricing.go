package web

// Dollar pricing for the per-attempt cache panel (drvweb-018). The daemon runs on
// a subscription OAuth session, so these figures are a MODELLED "equivalent API
// cost", not an invoice line — the panel labels them as such. The single input
// $/token rate turns the cache's token-equivalent saving (agent.Metrics'
// CostAvoidedInputTokens / BilledInputTokens, already input-rate-normalised) into a
// dollar figure the rest of the org can read.

import (
	"fmt"
	"math"
)

// inputRatePerToken returns the model's input-token price in dollars and ok=true
// when the model id is known; ok=false leaves the panel on token-equivalents with
// no "$". Keyed by the id carried on the attempt (attempt.md `model:`), which is
// stored short ("opus-4.8"); the agent layer's pinned long id ("claude-opus-4-8")
// maps to the same rate. Anthropic lists opus-4.8 input at $5 per 1M tokens. Kept
// deliberately minimal (drvweb-018 scope) — an unknown model is a display
// downgrade, never a crash. Extend the switch as more models are pinned.
func inputRatePerToken(model string) (rate float64, ok bool) {
	const perMillion = 1.0 / 1_000_000
	switch model {
	case "opus-4.8", "claude-opus-4-8", "claude-opus-4-8[1m]":
		return 5.0 * perMillion, true
	default:
		return 0, false
	}
}

// dollars formats a dollar amount for the cache panel: two decimals normally, but
// four for a sub-cent magnitude so a real-but-tiny saving does not collapse to
// "$0.00". The sign leads the "$" ("-$0.42") so a negative cost-avoided figure —
// the write-only silent-invalidator signal — reads as a loss, not a gain.
func dollars(f float64) string {
	neg := f < 0
	a := math.Abs(f)
	var s string
	if a != 0 && a < 0.01 {
		s = fmt.Sprintf("$%.4f", a)
	} else {
		s = fmt.Sprintf("$%.2f", a)
	}
	if neg {
		return "-" + s
	}
	return s
}
