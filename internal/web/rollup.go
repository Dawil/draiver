package web

// Cross-attempt, per-repo prompt-cache rollup (drvweb-013). The per-attempt panel
// (drvweb-012) can only show one session's tally, but the caching win is
// cross-ticket amortisation: every attempt after the first on a repo reads the
// shared prefix at ~10%. That is inherently a rollup across the attempts sharing a
// repo, so this file aggregates each attempt's folded-in agent.Metrics per repo and
// splits them into flag-on / flag-off A/B cohorts.
//
// Aggregation is sum-then-derive: raw token counts are summed into an agent.Totals
// and the derived metrics (hit ratio, normalised work, billed-input) are computed
// on the sum. Averaging per-attempt ratios would weight a tiny attempt the same as a
// huge one; summing the tokens first is the only correct way to roll them up.

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/project"
)

// unsetRepo buckets attempts that record no repo path, so nothing is silently
// dropped from the rollup (an attempt with no metrics is counted as Pending instead).
const unsetRepo = "(unset)"

// creationDominateThreshold is the cache_creation share above which a repo's caching
// health flips to a warning: when writes meet or exceed reads across a repo's
// attempts, the shared prefix is being rewritten more than it is read back — the
// silent-invalidator signature (a per-ticket value leaked into the append, or an
// unpinned adapter bump). Below it, reads dominate and the prefix is amortising.
const creationDominateThreshold = 0.5

// rollupVM drives the /cache page. Repos is the per-repo rollup (sorted by path);
// Pending counts attempts with no folded-in metrics yet (in-flight, or retired
// without a metered session) so the totals read honestly as "over N metered
// attempts" rather than implying every attempt contributed.
type rollupVM struct {
	Repos       []repoRollupVM
	Metered     int // attempts that contributed metrics
	Pending     int // attempts skipped for want of metrics
	FaviconHref string
}

// repoRollupVM is one repo's aggregate cache metrics plus its A/B cohorts. The
// aggregate spans every metered attempt on the repo (both cohorts); On/Off split
// them by whether caching engaged.
type repoRollupVM struct {
	Repo     string
	Attempts int // metered attempts folded into this repo

	HitRatioPct    string
	NormalizedWork string
	BilledInput    string
	UncachedInput  string
	SavedPct       string
	CacheRead      string
	CacheCreation  string

	Health healthVM
	On     cohortVM // caching engaged (CachingActive)
	Off    cohortVM // caching never engaged
}

// healthVM is the per-repo caching-health line. Status is "ok" (reads dominate),
// "warn" (cache_creation dominates — the silent-invalidator smell), or "off"
// (caching never engaged on this repo, so there is nothing to be healthy about).
// CreationSharePct is cache_creation / (cache_creation + cache_read).
type healthVM struct {
	Status           string
	Label            string
	CreationSharePct string
}

// cohortVM is one A/B cohort's aggregate. Present is false when the cohort has no
// metered attempts, so the template mutes it rather than rendering a row of zeros.
type cohortVM struct {
	Present        bool
	Attempts       int
	NormalizedWork string
	BilledInput    string
	HitRatioPct    string
	SavedPct       string
	Outcome        outcomeVM
}

// outcomeVM is a cohort's terminal-state distribution — the "comparable on outcome"
// half of the A/B: whether caching-on attempts reach Done/Review at the same rate as
// caching-off ones. Summary is the pre-formatted line; the counts back it for tests.
type outcomeVM struct {
	Done    int
	Review  int
	Running int
	NeedsMe int
	Summary string
}

// cohortRowVM tags a cohortVM with the kind/label the "cohort" partial needs, so
// the template renders both A/B columns from one logic-free partial. cohortVM is
// embedded, so the partial reads its fields (.Present, .Attempts, …) directly.
type cohortRowVM struct {
	Kind  string // "on" | "off" — drives the data-testid and colour class
	Label string
	cohortVM
}

// cohortRow is the template helper behind {{template "cohort" (cohortRow …)}}.
func cohortRow(kind, label string, c cohortVM) cohortRowVM {
	return cohortRowVM{Kind: kind, Label: label, cohortVM: c}
}

// cohortAcc sums one cohort's raw token counts and tallies its attempts' outcomes.
type cohortAcc struct {
	totals   agent.Totals
	n        int
	outcomes map[project.State]int
}

func (c *cohortAcc) add(m agent.Metrics, st project.State) {
	c.totals.InputTokens += m.InputTokens
	c.totals.OutputTokens += m.OutputTokens
	c.totals.CacheReadTokens += m.CacheReadTokens
	c.totals.CacheCreationTokens += m.CacheCreationTokens
	c.n++
	if c.outcomes == nil {
		c.outcomes = map[project.State]int{}
	}
	c.outcomes[st]++
}

// repoAcc accumulates a repo's attempts: the whole-repo aggregate plus the two
// cohorts. all == on merged with off, kept separately so the aggregate row need not
// re-merge.
type repoAcc struct {
	all cohortAcc
	on  cohortAcc
	off cohortAcc
}

// repoRollups aggregates attempts into the /cache view model: per repo, then split
// into caching-on / caching-off cohorts by each attempt's CachingActive. Attempts
// with no folded-in metrics are counted as Pending and otherwise skipped — they have
// no cohort (CachingActive is only known once metrics land). Pure and
// deterministic (repos sorted by path), so it is exercised directly in tests.
func repoRollups(attempts []project.Attempt) rollupVM {
	repos := map[string]*repoAcc{}
	var order []string
	var pending int

	for _, a := range attempts {
		if a.Metrics == nil {
			pending++
			continue
		}
		key := a.Repo
		if key == "" {
			key = unsetRepo
		}
		ra := repos[key]
		if ra == nil {
			ra = &repoAcc{}
			repos[key] = ra
			order = append(order, key)
		}
		m := *a.Metrics
		ra.all.add(m, a.State)
		if m.CachingActive {
			ra.on.add(m, a.State)
		} else {
			ra.off.add(m, a.State)
		}
	}
	sort.Strings(order)

	vm := rollupVM{Pending: pending}
	for _, key := range order {
		ra := repos[key]
		vm.Metered += ra.all.n

		am := ra.all.totals.Metrics()
		uncached, saved := cacheCost(am)
		vm.Repos = append(vm.Repos, repoRollupVM{
			Repo:           key,
			Attempts:       ra.all.n,
			HitRatioPct:    pct(am.CacheHitRatio),
			NormalizedWork: groupInt(am.NormalizedWork),
			BilledInput:    groupInt(roundTokens(am.BilledInputTokens)),
			UncachedInput:  groupInt(uncached),
			SavedPct:       saved,
			CacheRead:      groupInt(am.CacheReadTokens),
			CacheCreation:  groupInt(am.CacheCreationTokens),
			Health:         healthOf(am),
			On:             cohortProjection(ra.on),
			Off:            cohortProjection(ra.off),
		})
	}
	return vm
}

// cohortProjection renders one cohort's summed totals. A cohort with no attempts
// yields a zero cohortVM (Present false) so the template can mute it.
func cohortProjection(c cohortAcc) cohortVM {
	if c.n == 0 {
		return cohortVM{}
	}
	m := c.totals.Metrics()
	_, saved := cacheCost(m)
	return cohortVM{
		Present:        true,
		Attempts:       c.n,
		NormalizedWork: groupInt(m.NormalizedWork),
		BilledInput:    groupInt(roundTokens(m.BilledInputTokens)),
		HitRatioPct:    pct(m.CacheHitRatio),
		SavedPct:       saved,
		Outcome:        outcomeOf(c.outcomes),
	}
}

// healthOf reads the caching-health signal off a repo's aggregate metrics. "off"
// when caching never engaged; otherwise "warn" when cache_creation meets or exceeds
// reads (the shared prefix is being rewritten, not amortised) and "ok" when reads win.
func healthOf(m agent.Metrics) healthVM {
	if !m.CachingActive {
		return healthVM{Status: "off", Label: "caching not active on this repo", CreationSharePct: "—"}
	}
	written := m.CacheReadTokens + m.CacheCreationTokens
	var share float64
	if written > 0 {
		share = float64(m.CacheCreationTokens) / float64(written)
	}
	if share >= creationDominateThreshold {
		return healthVM{
			Status:           "warn",
			Label:            "cache_creation dominates — the shared prefix may be busting",
			CreationSharePct: pct(share),
		}
	}
	return healthVM{
		Status:           "ok",
		Label:            "healthy — cache reads dominate writes",
		CreationSharePct: pct(share),
	}
}

// outcomeOf tallies a cohort's attempts by terminal state into a pre-formatted line
// (fixed Done → Review → Running → Needs-me order, zero states omitted), backed by
// the counts for tests. "—" when the cohort is empty.
func outcomeOf(counts map[project.State]int) outcomeVM {
	o := outcomeVM{
		Done:    counts[project.Done],
		Review:  counts[project.Review],
		Running: counts[project.Running],
		NeedsMe: counts[project.NeedsMe],
	}
	var parts []string
	for _, st := range []project.State{project.Done, project.Review, project.Running, project.NeedsMe} {
		if n := counts[st]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, st))
		}
	}
	o.Summary = strings.Join(parts, " · ")
	if o.Summary == "" {
		o.Summary = "—"
	}
	return o
}

// cacheCost projects the billed-vs-uncached input-rate cost of a metrics tally:
// uncached is every prompt token (input + reads + writes) at the full input rate —
// what the session would have billed with no cache — and savedPct is the fraction the
// cache shaved off the bill, or "—" when there were no prompt tokens to bill. Shared
// by the per-attempt panel (cachePanel) and every rollup aggregate so the "saved"
// figure means the same thing everywhere.
func cacheCost(m agent.Metrics) (uncached int, savedPct string) {
	uncached = m.InputTokens + m.CacheReadTokens + m.CacheCreationTokens
	savedPct = "—"
	if uncached > 0 {
		savedPct = pct((float64(uncached) - m.BilledInputTokens) / float64(uncached))
	}
	return uncached, savedPct
}

// pct formats a 0..1 fraction as a one-decimal percentage ("0.732" → "73.2%").
func pct(frac float64) string { return fmt.Sprintf("%.1f%%", frac*100) }

// roundTokens rounds a fractional billed-token figure to the nearest whole token for
// display (billed-input is fractional because the cache multipliers are).
func roundTokens(f float64) int { return int(math.Round(f)) }
