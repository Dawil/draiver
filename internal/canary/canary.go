// Package canary is draiver's silent-invalidator alarm (drvctl-036): it watches
// for a busted shared prompt-cache prefix across the attempts of a repo. When the
// control plane strips the dynamic system-prompt sections (drvctl-032's
// --exclude-dynamic-system-prompt-sections) and pins the adapter (drvctl-033),
// every attempt on a repo shares one byte-identical tools+system+append prefix, so
// the *second* and later attempts should turn-1 *read* that prefix from cache
// rather than re-create it. A silent invalidator — per-ticket data leaking into
// the append, or an unpinned adapter upgrade — re-fragments the prefix, and every
// attempt cold-writes it again. That erases the cross-ticket cache win without any
// error, hence "silent".
//
// # Why turn-1, not the aggregate meter
//
// The obvious signal — "cache_creation stays high across attempts" read off the
// session-cumulative meter (drvctl-031) — does not work in practice. Within a
// single session, turn-to-turn caching already dominates the totals, so aggregate
// cache_creation share is uniformly low (empirically 0.001–0.10 across draiver's
// own flag-off history) whether or not the *cross-attempt* prefix is shared. The
// bust is visible only on the first request of a session: a warm shared prefix
// shows up there as cache_read, a busted one as cache_creation (or uncached
// input). So the canary judges each attempt's turn-1 usage frame, not its meter.
//
// The analysis is a pure function of []Observation; the disk loader (load.go)
// populates observations from the store, and cmd/canary.go renders them. Keeping
// Analyze pure is what lets the acceptance test drive intentionally-busted and
// healthy fixtures through it with no I/O.
package canary

import (
	"fmt"
	"sort"
	"time"

	"github.com/Dawil/draiver/internal/agent"
)

// Observation is one attempt's cache evidence, as the canary needs it: which repo
// it ran against, when it started (to order attempts on a repo), its turn-1 usage
// frame (the cross-attempt sharing tell), its session-cumulative totals (for the
// rollup and the caching-never-engaged hard miss), and the adapter version it ran
// (drvctl-033; empty until version events exist, and the canary degrades to
// "unknown" attribution rather than failing).
type Observation struct {
	Repo           string
	Ticket         string
	Attempt        string
	StartedAt      time.Time
	FirstTurn      agent.Usage  // first per-request usage frame of the session (turn-1)
	HasFirstTurn   bool         // false when no usage frame was recorded (stream absent/empty)
	Totals         agent.Totals // session-cumulative tally (drvctl-031)
	AdapterVersion string       // drvctl-033 version event; "" when unknown
}

// Config holds the (documented, tunable) thresholds. Real turn-1 data carries
// caching noise, so these are heuristic knobs, not physical constants; the
// acceptance fixtures are chosen to sit well clear of any plausible setting.
type Config struct {
	// MinSharedPrefixTokens: a non-first attempt whose turn-1 cache_read is below
	// this is treated as "did not reuse the shared prefix". The cacheable minimum
	// is ~1K tokens by model; the real tools+system+append prefix is comfortably
	// larger, so a genuine reuse reads far more than this and a bust reads ~0.
	MinSharedPrefixTokens int
	// ReadShareFloor: turn-1 read share = read/(read+creation+input). A warm shared
	// prefix pushes this high (the prefix is read, not written); a cold/busted
	// turn-1 sits low. Below the floor counts as cold.
	ReadShareFloor float64
	// MinAttempts: a repo needs at least this many observed attempts before the
	// canary will judge cross-attempt sharing (you cannot share with yourself).
	MinAttempts int
	// ColdFraction: a repo fires when this share of its non-first attempts are cold.
	// 0.5 = a majority; with a single non-first attempt, one cold attempt fires.
	ColdFraction float64
}

// DefaultConfig is the shipping default.
func DefaultConfig() Config {
	return Config{
		MinSharedPrefixTokens: 1024,
		ReadShareFloor:        0.5,
		MinAttempts:           2,
		ColdFraction:          0.5,
	}
}

// AttemptResult is one attempt's row in the per-repo rollup, plus the canary's
// verdict on it.
type AttemptResult struct {
	Ticket    string
	Attempt   string
	StartedAt time.Time
	Baseline  bool // the earliest attempt on the repo: the expected cold prefix-writer

	// turn-1 evidence
	HasFirstTurn      bool
	FirstTurnRead     int
	FirstTurnCreation int
	FirstTurnInput    int
	ReadShare         float64

	// verdict
	Cold            bool // non-baseline attempt that failed to reuse the shared prefix
	CachingInactive bool // Totals show caching never engaged at all (silent-invalidator hard miss)

	// rollup (session-cumulative, from drvctl-031 metrics)
	NormalizedWork int
	BilledInput    float64
	HitRatio       float64
	CachingActive  bool

	AdapterVersion string
}

// RepoResult is the canary's finding for one repo.
type RepoResult struct {
	Repo     string
	Attempts []AttemptResult // ordered earliest-first
	Judged   bool            // false when the repo had fewer than MinAttempts observations
	Fired    bool
	Findings []string // human-readable reasons the canary fired (empty when quiet)
}

// Report is the whole scan.
type Report struct {
	Repos []RepoResult
}

// Fired reports whether the canary fired on any repo — the process exit signal.
func (r Report) Fired() bool {
	for _, repo := range r.Repos {
		if repo.Fired {
			return true
		}
	}
	return false
}

// readShare is turn-1 read/(read+creation+input): the share of the first request's
// prompt tokens served from cache. Zero when the first turn saw no prompt tokens.
func readShare(u agent.Usage) float64 {
	denom := u.CacheReadTokens + u.CacheCreationTokens + u.InputTokens
	if denom == 0 {
		return 0
	}
	return float64(u.CacheReadTokens) / float64(denom)
}

// Analyze groups observations by repo, orders each repo's attempts in time, and
// judges whether the shared prefix is being reused across them. It is a pure
// function: the acceptance test feeds it busted and healthy fixtures directly.
func Analyze(obs []Observation, cfg Config) Report {
	byRepo := map[string][]Observation{}
	var order []string
	for _, o := range obs {
		if _, seen := byRepo[o.Repo]; !seen {
			order = append(order, o.Repo)
		}
		byRepo[o.Repo] = append(byRepo[o.Repo], o)
	}
	sort.Strings(order)

	var report Report
	for _, repo := range order {
		report.Repos = append(report.Repos, analyzeRepo(repo, byRepo[repo], cfg))
	}
	return report
}

func analyzeRepo(repo string, obs []Observation, cfg Config) RepoResult {
	// Earliest attempt first; the earliest is the baseline cold-writer. Ties broken
	// by ticket/attempt id so the ordering is deterministic.
	sort.SliceStable(obs, func(i, j int) bool {
		if !obs[i].StartedAt.Equal(obs[j].StartedAt) {
			return obs[i].StartedAt.Before(obs[j].StartedAt)
		}
		if obs[i].Ticket != obs[j].Ticket {
			return obs[i].Ticket < obs[j].Ticket
		}
		return obs[i].Attempt < obs[j].Attempt
	})

	res := RepoResult{Repo: repo, Judged: len(obs) >= cfg.MinAttempts}

	var baselineVersion string
	nonBaseline, cold := 0, 0
	for i, o := range obs {
		ar := AttemptResult{
			Ticket:         o.Ticket,
			Attempt:        o.Attempt,
			StartedAt:      o.StartedAt,
			Baseline:       i == 0,
			HasFirstTurn:   o.HasFirstTurn,
			AdapterVersion: o.AdapterVersion,
			NormalizedWork: o.Totals.NormalizedWork(),
			BilledInput:    o.Totals.BilledInputTokens(),
			HitRatio:       o.Totals.CacheHitRatio(),
			CachingActive:  o.Totals.CachingActive(),
		}
		if o.HasFirstTurn {
			ar.FirstTurnRead = o.FirstTurn.CacheReadTokens
			ar.FirstTurnCreation = o.FirstTurn.CacheCreationTokens
			ar.FirstTurnInput = o.FirstTurn.InputTokens
			ar.ReadShare = readShare(o.FirstTurn)
		}

		if i == 0 {
			baselineVersion = o.AdapterVersion
		}

		// Caching never engaged at all — the below-min-prefix / hard silent-
		// invalidator miss. Applies to any attempt, baseline included (drvctl-031
		// caching_active).
		if !o.Totals.CachingActive() && o.Totals.NormalizedWork() > 0 {
			ar.CachingInactive = true
			where := ""
			if i == 0 {
				where = " [baseline]"
			}
			res.Findings = append(res.Findings, fmt.Sprintf(
				"%s@%s: caching never engaged (caching_active=false over %d tokens of work) — prefix below the cache minimum or a hard invalidator%s",
				o.Ticket, o.Attempt, o.Totals.NormalizedWork(), where))
		}

		// Cross-attempt reuse is only meaningful for the non-first attempts: the
		// baseline is expected to cold-write the prefix.
		if i > 0 {
			nonBaseline++
			if reused := o.HasFirstTurn &&
				o.FirstTurn.CacheReadTokens >= cfg.MinSharedPrefixTokens &&
				ar.ReadShare >= cfg.ReadShareFloor; !reused {
				ar.Cold = true
				cold++
				res.Findings = append(res.Findings, coldFinding(o, ar, baselineVersion, cfg))
			}
		}
		res.Attempts = append(res.Attempts, ar)
	}

	// Fire when the repo is judged and either a majority of its non-first attempts
	// failed to reuse the prefix, or any attempt shows caching wholly inactive.
	if res.Judged {
		if nonBaseline > 0 && float64(cold)/float64(nonBaseline) >= cfg.ColdFraction {
			res.Fired = true
		}
		for _, ar := range res.Attempts {
			if ar.CachingInactive {
				res.Fired = true
			}
		}
	}
	return res
}

// coldFinding renders the reason a non-first attempt failed to reuse the prefix,
// attributing it to an adapter-version change when the drvctl-033 version events
// are available and degrading to "unknown" when they are not.
func coldFinding(o Observation, ar AttemptResult, baselineVersion string, cfg Config) string {
	base := fmt.Sprintf(
		"%s@%s: turn-1 did not reuse the shared prefix (read=%d, creation=%d, input=%d, read-share=%.2f; want read≥%d and share≥%.2f)",
		o.Ticket, o.Attempt, ar.FirstTurnRead, ar.FirstTurnCreation, ar.FirstTurnInput,
		ar.ReadShare, cfg.MinSharedPrefixTokens, cfg.ReadShareFloor)
	switch {
	case !o.HasFirstTurn:
		return fmt.Sprintf("%s@%s: no turn-1 usage frame recorded — cannot confirm prefix reuse", o.Ticket, o.Attempt)
	case baselineVersion == "" || o.AdapterVersion == "":
		return base + " — adapter version unavailable (drvctl-033 version events not yet recorded), cannot attribute"
	case baselineVersion != o.AdapterVersion:
		return base + fmt.Sprintf(" — adapter version changed %s→%s (unpinned upgrade; pin the adapter per drvctl-033)", baselineVersion, o.AdapterVersion)
	default:
		return base + " — adapter version unchanged, so likely per-ticket data in the --append-system-prompt or a sub-minimum prefix"
	}
}
