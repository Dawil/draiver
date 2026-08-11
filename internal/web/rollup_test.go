package web

import (
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// metricsOf builds a folded-in agent.Metrics the way retire does — from summed
// raw token totals — so tests exercise the same derived fields (CachingActive,
// hit ratio, billed) the rollup reads.
func metricsOf(input, output, read, creation int) *agent.Metrics {
	m := agent.Totals{
		InputTokens: input, OutputTokens: output,
		CacheReadTokens: read, CacheCreationTokens: creation,
	}.Metrics()
	return &m
}

func attemptWith(repo string, st project.State, m *agent.Metrics) project.Attempt {
	return project.Attempt{Repo: repo, State: st, Metrics: m}
}

// TestRepoRollupsGroupsAggregatesAndSplitsCohorts is the core pure-function test:
// attempts group by repo (sorted), the aggregate sums tokens then derives, cohorts
// split on CachingActive, and a metrics-less attempt counts as Pending, not a cohort.
func TestRepoRollupsGroupsAggregatesAndSplitsCohorts(t *testing.T) {
	attempts := []project.Attempt{
		// /repo/a: one caching-on (reads dominate) Done, one caching-off Review.
		attemptWith("/repo/a", project.Done, metricsOf(100, 20, 900, 100)),
		attemptWith("/repo/a", project.Review, metricsOf(500, 50, 0, 0)),
		// /repo/a: an in-flight attempt with no metrics — Pending, no cohort.
		attemptWith("/repo/a", project.Running, nil),
		// /repo/b: a single caching-on Running attempt.
		attemptWith("/repo/b", project.Running, metricsOf(200, 10, 50, 50)),
	}

	vm := repoRollups(attempts)

	if vm.Metered != 3 {
		t.Errorf("Metered = %d, want 3", vm.Metered)
	}
	if vm.Pending != 1 {
		t.Errorf("Pending = %d, want 1", vm.Pending)
	}
	if len(vm.Repos) != 2 {
		t.Fatalf("len(Repos) = %d, want 2", len(vm.Repos))
	}
	// Sorted by path.
	if vm.Repos[0].Repo != "/repo/a" || vm.Repos[1].Repo != "/repo/b" {
		t.Fatalf("repos not sorted: %q, %q", vm.Repos[0].Repo, vm.Repos[1].Repo)
	}

	a := vm.Repos[0]
	if a.Attempts != 2 {
		t.Errorf("/repo/a metered attempts = %d, want 2 (pending excluded)", a.Attempts)
	}
	// Aggregate normalised work = sum of every token across both cohorts:
	// (100+20+900+100) + (500+50) = 1670.
	if a.NormalizedWork != "1,670" {
		t.Errorf("/repo/a normalised work = %q, want %q", a.NormalizedWork, "1,670")
	}
	// Reads (900) dominate writes (100) → healthy.
	if a.Health.Status != "ok" {
		t.Errorf("/repo/a health = %q, want ok", a.Health.Status)
	}

	if !a.On.Present || a.On.Attempts != 1 {
		t.Errorf("/repo/a on-cohort = %+v, want 1 present", a.On)
	}
	if a.On.NormalizedWork != "1,120" { // 100+20+900+100
		t.Errorf("/repo/a on normalised work = %q, want %q", a.On.NormalizedWork, "1,120")
	}
	if a.On.Outcome.Summary != "1 Done" {
		t.Errorf("/repo/a on outcome = %q, want %q", a.On.Outcome.Summary, "1 Done")
	}
	if !a.Off.Present || a.Off.Attempts != 1 {
		t.Errorf("/repo/a off-cohort = %+v, want 1 present", a.Off)
	}
	if a.Off.NormalizedWork != "550" { // 500+50
		t.Errorf("/repo/a off normalised work = %q, want %q", a.Off.NormalizedWork, "550")
	}
	if a.Off.Outcome.Summary != "1 Review" {
		t.Errorf("/repo/a off outcome = %q, want %q", a.Off.Outcome.Summary, "1 Review")
	}

	b := vm.Repos[1]
	if !b.On.Present || b.Off.Present {
		t.Errorf("/repo/b cohorts = on:%v off:%v, want on-only", b.On.Present, b.Off.Present)
	}
}

// TestRepoRollupsHealthTransitions pins the three caching-health states: reads
// dominate → ok, writes dominate → warn (silent-invalidator smell), no cache at
// all → off.
func TestRepoRollupsHealthTransitions(t *testing.T) {
	cases := []struct {
		name       string
		m          *agent.Metrics
		wantStatus string
	}{
		{"reads dominate", metricsOf(0, 0, 900, 100), "ok"},
		{"writes dominate", metricsOf(0, 0, 100, 900), "warn"},
		{"caching off", metricsOf(500, 50, 0, 0), "off"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vm := repoRollups([]project.Attempt{attemptWith("/r", project.Done, c.m)})
			if got := vm.Repos[0].Health.Status; got != c.wantStatus {
				t.Errorf("health = %q, want %q", got, c.wantStatus)
			}
		})
	}
}

// TestRepoRollupsBucketsUnsetRepo pins that an attempt recording no repo path is
// bucketed under "(unset)" rather than silently dropped.
func TestRepoRollupsBucketsUnsetRepo(t *testing.T) {
	vm := repoRollups([]project.Attempt{attemptWith("", project.Done, metricsOf(10, 1, 5, 5))})
	if len(vm.Repos) != 1 || vm.Repos[0].Repo != unsetRepo {
		t.Fatalf("unset repo not bucketed: %+v", vm.Repos)
	}
}

// --- handler / template rendering ---

// seedMetered writes an attempt.md carrying repo + a folded-in metrics block, on
// top of a created (and optional extra) event so the attempt has a control state.
func seedMetered(t *testing.T, root store.Root, id, att, repo string, m *agent.Metrics, evs ...event.Event) {
	t.Helper()
	if err := root.EnsureAttemptDirs(id, att); err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, id, att, event.Event{Type: "created", Actor: "a", Body: "start"}); err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if _, err := ticketlog.Append(root, id, att, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := attempt.WriteMeta(root, attempt.Meta{ID: att, Ticket: id, Repo: repo, Metrics: m}); err != nil {
		t.Fatal(err)
	}
}

func TestCacheRollupPageRenders(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	// /work/x: a Review attempt with caching engaged (reads dominate).
	seedMetered(t, root, "PROJ-1", "0001", "/work/x", metricsOf(100, 20, 900, 100),
		event.Event{Type: "review", Actor: "agent:x", Body: "done"})
	// /work/x: a second, caching-off attempt (still Running).
	seedMetered(t, root, "PROJ-2", "0001", "/work/x", metricsOf(500, 50, 0, 0))

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	rr := get(t, s.Handler(), "/cache")
	if rr.Code != 200 {
		t.Fatalf("GET /cache = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`data-testid="cache-rollup"`,
		`data-testid="repo-/work/x"`,
		`data-testid="repo-health"`,
		`data-testid="repo-normalized-work"`,
		`data-testid="cohort-on"`,
		`data-testid="cohort-off"`,
		`data-testid="cohort-on-attempts"`,
		`data-testid="cohort-off-outcome"`,
		`/work/x`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /cache missing %q", want)
		}
	}
}

// TestCacheRollupEmpty pins the quiet state: with no metered attempts the page
// still renders (200) and shows the placeholder plus the pending count, never a
// grid of zeros.
func TestCacheRollupEmpty(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("PROJ-1", "0001"); err != nil {
		t.Fatal(err)
	}
	ticketlog.Append(root, "PROJ-1", "0001", event.Event{Type: "created", Actor: "a", Body: "start"})

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	rr := get(t, s.Handler(), "/cache")
	if rr.Code != 200 {
		t.Fatalf("GET /cache = %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-testid="rollup-empty"`) {
		t.Errorf("empty rollup missing the placeholder note")
	}
	if !strings.Contains(body, `data-testid="rollup-pending"`) {
		t.Errorf("empty rollup should surface the 1 pending attempt")
	}
}

// TestBoardLinksToCacheRollup pins the entry point: the board topbar links to /cache.
func TestBoardLinksToCacheRollup(t *testing.T) {
	h := newServer(t)
	body := get(t, h, "/").Body.String()
	for _, want := range []string{`data-testid="cache-rollup-link"`, `href="/cache"`} {
		if !strings.Contains(body, want) {
			t.Errorf("board missing %q", want)
		}
	}
}
