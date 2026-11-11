package web

import (
	"sort"
	"strconv"
	"time"

	"github.com/Dawil/draiver/internal/project"
)

// metrics.go computes the flow-metrics strip (drvweb-027): four at-a-glance numbers
// about how the flow itself is doing, folded onto boardVM and rendered inside the
// polled board region so they refresh on the 3s poll with no extra wiring. It is a
// PURE projection over the attempts board() already loaded — no new disk read, and
// no render-time time.Now(): the clock is threaded in (s.now), so the math is
// unit-testable with a fixed clock.
//
// All four are computed over CODE tickets only — archived attempts and codebase
// ledgers are excluded. A ledger never reaches Done (restingState rewrites its Done
// back to Parked), so it would sit in "unresolved"/"Oldest" forever and skew every
// number; and an archived attempt is off the board entirely.

// throughputWindow is the 4-Week Throughput lookback: a code ticket counts if it
// reached Done within this window of `now`. 28 days exactly (the AC's boundary: a
// ticket done 27 days ago counts, 29 days ago does not).
const throughputWindow = 28 * 24 * time.Hour

// devCycleFloor is the minimum number of resolved samples for an Expected Dev-Cycle
// to be meaningful; below it the tile shows an em dash rather than a noisy p85.
const devCycleFloor = 3

// metricsVM is the flow-metrics strip's view model, folded onto boardVM.
type metricsVM struct {
	// OldestHasData is false when the unresolved set is empty — the Oldest tile then
	// shows an em dash and names no ticket.
	OldestHasData bool
	// OldestAge is the humanised age (now - created) of the oldest unresolved code
	// ticket, rendered via relativeAge ("3 days ago").
	OldestAge string
	// OldestTicket is the ticket id that age belongs to, named beside the tile.
	OldestTicket string

	// ExpectedDevCycle is the p85 of resolved tickets' created→done durations as a
	// humanised duration ("3 days"), or "—" below devCycleFloor samples.
	ExpectedDevCycle string

	// TotalUnresolved is the size of the unresolved set — the population behind
	// Oldest, so the two are consistent by construction.
	TotalUnresolved int

	// Throughput4w is the count of code tickets whose done is within throughputWindow.
	Throughput4w int
}

// emDash is the placeholder a metric shows when it has no meaningful value.
const emDash = "—"

// computeMetrics folds the four flow metrics over the attempts board() already
// loaded, as of `now`. It is pure: it reads only the in-memory events, never the
// clock or disk, so a fixed `now` makes every metric deterministic under test.
func computeMetrics(attempts []project.Attempt, now time.Time) metricsVM {
	var m metricsVM
	var oldestCreated time.Time // the earliest created among unresolved (= oldest age)
	var devCycles []time.Duration

	for _, a := range attempts {
		// Code tickets only: exclude archived attempts and codebase ledgers.
		if a.Archived || a.Type == project.TypeCodebase {
			continue
		}
		created, ok := attemptCreated(a)
		if !ok {
			continue // no created event — cannot place it on any age/cycle axis
		}

		// Resolved = board state Done. Control()/BoardState() only ever map
		// Running→Pending or NeedsMe→Pending, never into or out of Done, so the
		// runtime Desired/Live fold cannot change Done-ness: a.State suffices.
		if a.State == project.Done {
			if doneTS, ok := attemptDoneTS(a); ok {
				devCycles = append(devCycles, doneTS.Sub(created))
				if now.Sub(doneTS) < throughputWindow {
					m.Throughput4w++
				}
			}
			continue
		}

		// Unresolved: every non-archived, non-ledger code ticket that is not Done
		// (Running, Pending, Needs-me, Review — a Review claim is a claim, not a
		// resolution).
		m.TotalUnresolved++
		if !m.OldestHasData || created.Before(oldestCreated) {
			m.OldestHasData = true
			oldestCreated = created
			m.OldestTicket = a.Ticket
		}
	}

	if m.OldestHasData {
		m.OldestAge = relativeAge(now.Sub(oldestCreated))
	}
	m.ExpectedDevCycle = expectedDevCycle(devCycles)
	return m
}

// expectedDevCycle renders the 85th-percentile dev-cycle as a humanised duration,
// or an em dash below devCycleFloor samples. Percentile method: nearest-rank — sort
// ascending, rank = ceil(0.85 * N), take the value at index rank-1 (clamped). For
// N=10 that is the 9th-smallest value; for N=3 the 3rd (largest).
func expectedDevCycle(cycles []time.Duration) string {
	if len(cycles) < devCycleFloor {
		return emDash
	}
	sorted := make([]time.Duration, len(cycles))
	copy(sorted, cycles)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return humanDuration(percentileNearestRank(sorted, 0.85))
}

// percentileNearestRank returns the nearest-rank p-th percentile of an
// ascending-sorted, non-empty slice. rank = ceil(p*N), index = rank-1, clamped to
// a valid index so p at the extremes stays in range.
func percentileNearestRank(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	// ceil(p*n) without importing math: integer arithmetic on the ratio.
	rank := int(p*float64(n) + 0.9999999999)
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// attemptCreated returns an attempt's created timestamp — the seq-1 `created`
// event's TS (Events[0].TS) — guarding an empty Events slice.
func attemptCreated(a project.Attempt) (time.Time, bool) {
	if len(a.Events) == 0 {
		return time.Time{}, false
	}
	return a.Events[0].TS, true
}

// attemptDoneTS returns the TS of an attempt's `done` lifecycle event (the last one
// if several exist), or ok=false when none is present.
func attemptDoneTS(a project.Attempt) (time.Time, bool) {
	var ts time.Time
	found := false
	for _, e := range a.Events {
		if e.Type == "done" {
			ts = e.TS
			found = true
		}
	}
	return ts, found
}

// humanDuration renders a positive duration as a humanised length, mirroring
// relativeAge's bucket thresholds and vocabulary but WITHOUT the "ago" suffix — a
// dev-cycle is a length, not a time-ago. A non-positive duration shows "just now".
func humanDuration(d time.Duration) string {
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return durPlural(int(d.Seconds()), "second")
	case d < time.Hour:
		return durPlural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return durPlural(int(d.Hours()), "hour")
	case d < 7*24*time.Hour:
		return durPlural(int(d.Hours()/24), "day")
	case d < 30*24*time.Hour:
		return durPlural(int(d.Hours()/(24*7)), "week")
	case d < 365*24*time.Hour:
		return durPlural(int(d.Hours()/(24*30)), "month")
	default:
		return durPlural(int(d.Hours()/(24*365)), "year")
	}
}

// durPlural formats "N unit(s)" with singular/plural agreement (no "ago").
func durPlural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}
