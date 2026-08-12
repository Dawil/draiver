package canary

import (
	"strings"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
)

var epoch = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

func at(hours int) time.Time { return epoch.Add(time.Duration(hours) * time.Hour) }

// warmTotals is a realistic session-cumulative tally: caching is active and the
// hit ratio is high from within-session turn-to-turn caching. Crucially this is
// true for a *busted* cross-attempt prefix too — which is exactly why the canary
// judges turn-1, not the aggregate.
func warmTotals() agent.Totals {
	return agent.Totals{InputTokens: 100, OutputTokens: 200, CacheReadTokens: 500000, CacheCreationTokens: 20000}
}

// coldWriteTurn1 is the baseline's expected first request: it writes the prefix.
func coldWriteTurn1() agent.Usage {
	return agent.Usage{InputTokens: 500, CacheReadTokens: 0, CacheCreationTokens: 20000}
}

// warmReadTurn1 reads the shared prefix on the first request (healthy reuse).
func warmReadTurn1() agent.Usage {
	return agent.Usage{InputTokens: 50, CacheReadTokens: 20000, CacheCreationTokens: 1000}
}

// coldReadTurn1 re-creates the prefix on the first request (busted / no reuse).
func coldReadTurn1() agent.Usage {
	return agent.Usage{InputTokens: 500, CacheReadTokens: 0, CacheCreationTokens: 20000}
}

func obs(repo, ticket string, started time.Time, first agent.Usage, tot agent.Totals) Observation {
	return Observation{
		Repo: repo, Ticket: ticket, Attempt: "0001", StartedAt: started,
		FirstTurn: first, HasFirstTurn: true, Totals: tot,
	}
}

func TestQuietOnHealthyPrefix(t *testing.T) {
	// Baseline cold-writes; the two later attempts turn-1 read the shared prefix.
	o := []Observation{
		obs("/repo", "t-1", at(0), coldWriteTurn1(), warmTotals()),
		obs("/repo", "t-2", at(1), warmReadTurn1(), warmTotals()),
		obs("/repo", "t-3", at(2), warmReadTurn1(), warmTotals()),
	}
	rep := Analyze(o, DefaultConfig())
	if rep.Fired() {
		t.Fatalf("healthy prefix should be quiet, fired with findings: %v", rep.Repos[0].Findings)
	}
	if !rep.Repos[0].Judged {
		t.Fatal("repo with 3 attempts should be judged")
	}
	for _, ar := range rep.Repos[0].Attempts {
		if ar.Cold {
			t.Errorf("attempt %s wrongly marked cold (read-share %.2f)", ar.Ticket, ar.ReadShare)
		}
	}
}

func TestFiresOnBustedPrefix(t *testing.T) {
	// Every non-first attempt re-creates the prefix on turn-1 — the signature of a
	// busted shared prefix — even though session totals still show caching active.
	o := []Observation{
		obs("/repo", "t-1", at(0), coldWriteTurn1(), warmTotals()),
		obs("/repo", "t-2", at(1), coldReadTurn1(), warmTotals()),
		obs("/repo", "t-3", at(2), coldReadTurn1(), warmTotals()),
	}
	rep := Analyze(o, DefaultConfig())
	if !rep.Fired() {
		t.Fatal("busted prefix should fire")
	}
	repo := rep.Repos[0]
	if repo.Attempts[0].Cold {
		t.Error("baseline (first attempt) must never be marked cold — it is the expected writer")
	}
	if !repo.Attempts[1].Cold || !repo.Attempts[2].Cold {
		t.Error("both non-first attempts should be cold")
	}
	if len(repo.Findings) < 2 {
		t.Errorf("expected a finding per cold attempt, got %d: %v", len(repo.Findings), repo.Findings)
	}
}

func TestSilentInvalidatorCachingInactive(t *testing.T) {
	// A non-first attempt where caching never engaged at all (both cache fields
	// zero over real work) — the below-min-prefix / hard-invalidator miss.
	dead := agent.Totals{InputTokens: 40000, OutputTokens: 5000} // caching_active == false
	o := []Observation{
		obs("/repo", "t-1", at(0), coldWriteTurn1(), warmTotals()),
		{Repo: "/repo", Ticket: "t-2", Attempt: "0001", StartedAt: at(1), HasFirstTurn: false, Totals: dead},
	}
	rep := Analyze(o, DefaultConfig())
	if !rep.Fired() {
		t.Fatal("caching-inactive attempt should fire")
	}
	if !rep.Repos[0].Attempts[1].CachingInactive {
		t.Error("attempt t-2 should be flagged caching-inactive")
	}
	joined := strings.Join(rep.Repos[0].Findings, "\n")
	if !strings.Contains(joined, "caching never engaged") {
		t.Errorf("finding should name the caching-inactive miss, got: %v", rep.Repos[0].Findings)
	}
}

func TestVersionAttribution(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name          string
		baseVer, curV string
		wantSubstr    string
	}{
		{"version bumped", "1.2.0", "1.3.0", "adapter version changed 1.2.0→1.3.0"},
		{"version unchanged", "1.2.0", "1.2.0", "adapter version unchanged"},
		{"version unknown", "", "", "adapter version unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := obs("/repo", "t-1", at(0), coldWriteTurn1(), warmTotals())
			base.AdapterVersion = tc.baseVer
			cur := obs("/repo", "t-2", at(1), coldReadTurn1(), warmTotals())
			cur.AdapterVersion = tc.curV
			rep := Analyze([]Observation{base, cur}, cfg)
			if !rep.Fired() {
				t.Fatal("busted turn-1 should fire")
			}
			joined := strings.Join(rep.Repos[0].Findings, "\n")
			if !strings.Contains(joined, tc.wantSubstr) {
				t.Errorf("want finding to contain %q, got: %v", tc.wantSubstr, rep.Repos[0].Findings)
			}
		})
	}
}

func TestSingleAttemptRepoNotJudged(t *testing.T) {
	// You cannot share a prefix with yourself: one attempt on a repo is unjudgeable.
	o := []Observation{obs("/repo", "t-1", at(0), coldWriteTurn1(), warmTotals())}
	rep := Analyze(o, DefaultConfig())
	if rep.Fired() {
		t.Fatal("a single-attempt repo must not fire")
	}
	if rep.Repos[0].Judged {
		t.Fatal("a single-attempt repo must not be judged")
	}
}

func TestMultipleReposIsolated(t *testing.T) {
	// A busted repo fires without dragging a healthy repo down with it.
	o := []Observation{
		obs("/healthy", "h-1", at(0), coldWriteTurn1(), warmTotals()),
		obs("/healthy", "h-2", at(1), warmReadTurn1(), warmTotals()),
		obs("/busted", "b-1", at(0), coldWriteTurn1(), warmTotals()),
		obs("/busted", "b-2", at(1), coldReadTurn1(), warmTotals()),
	}
	rep := Analyze(o, DefaultConfig())
	if !rep.Fired() {
		t.Fatal("scan with a busted repo should fire")
	}
	byRepo := map[string]RepoResult{}
	for _, r := range rep.Repos {
		byRepo[r.Repo] = r
	}
	if byRepo["/healthy"].Fired {
		t.Error("/healthy should be quiet")
	}
	if !byRepo["/busted"].Fired {
		t.Error("/busted should fire")
	}
}

func TestOrderingByStartedAtPicksBaseline(t *testing.T) {
	// Observations arrive out of order; the earliest by StartedAt is the baseline
	// (the expected writer), regardless of input order.
	o := []Observation{
		obs("/repo", "late", at(2), coldReadTurn1(), warmTotals()),
		obs("/repo", "early", at(0), coldWriteTurn1(), warmTotals()),
		obs("/repo", "mid", at(1), coldReadTurn1(), warmTotals()),
	}
	rep := Analyze(o, DefaultConfig())
	if got := rep.Repos[0].Attempts[0].Ticket; got != "early" {
		t.Fatalf("baseline should be the earliest attempt 'early', got %q", got)
	}
	if !rep.Repos[0].Attempts[0].Baseline {
		t.Error("earliest attempt should be marked Baseline")
	}
	if rep.Repos[0].Attempts[0].Cold {
		t.Error("baseline must not be cold")
	}
}
