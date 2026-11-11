package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
)

// metrics_test.go unit-tests the flow-metrics math (drvweb-027) with a FIXED clock —
// no render-time time.Now() in the computation, so every metric is deterministic.

// fixedNow is the clock every case in this file is computed against.
var fixedNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// mkAttempt builds an in-memory code ticket whose created event is `createdAgo` before
// fixedNow. When doneAgo >= 0 it is Done with a `done` event that long before fixedNow;
// a negative doneAgo leaves it unresolved in `state`.
func mkMetricAttempt(ticket string, createdAgo time.Duration, state project.State, doneAgo time.Duration) project.Attempt {
	evs := []event.Event{{Seq: 1, Type: "created", TS: fixedNow.Add(-createdAgo)}}
	if doneAgo >= 0 {
		evs = append(evs, event.Event{Seq: 2, Type: "done", TS: fixedNow.Add(-doneAgo)})
		state = project.Done
	}
	return project.Attempt{Ticket: ticket, ID: "0001", Type: project.TypeTicket, State: state, Events: evs}
}

const day = 24 * time.Hour

func TestComputeMetrics_OldestAndTotalUnresolved(t *testing.T) {
	attempts := []project.Attempt{
		mkMetricAttempt("NEW", 2*day, project.Running, -1),
		mkMetricAttempt("OLD", 9*day, project.Review, -1),    // oldest unresolved; Review counts as unresolved
		mkMetricAttempt("MID", 5*day, project.NeedsMe, -1),   // unresolved
		mkMetricAttempt("DONE", 20*day, project.Done, 1*day), // resolved — must not be Oldest/unresolved
	}
	m := computeMetrics(attempts, fixedNow)

	if !m.OldestHasData {
		t.Fatal("expected Oldest to have data")
	}
	if m.OldestTicket != "OLD" {
		t.Errorf("OldestTicket = %q, want OLD", m.OldestTicket)
	}
	if want := relativeAge(9 * day); m.OldestAge != want {
		t.Errorf("OldestAge = %q, want %q", m.OldestAge, want)
	}
	if m.TotalUnresolved != 3 {
		t.Errorf("TotalUnresolved = %d, want 3 (NEW, OLD, MID)", m.TotalUnresolved)
	}
}

func TestComputeMetrics_ExcludesLedgersAndArchived(t *testing.T) {
	ledger := mkMetricAttempt("LEDGER", 30*day, project.Pending, -1)
	ledger.Type = project.TypeCodebase // a ledger never resolves — must be excluded
	archived := mkMetricAttempt("ARCH", 40*day, project.Running, -1)
	archived.Archived = true
	attempts := []project.Attempt{
		ledger,
		archived,
		mkMetricAttempt("REAL", 3*day, project.Running, -1),
	}
	m := computeMetrics(attempts, fixedNow)

	if m.TotalUnresolved != 1 || m.OldestTicket != "REAL" {
		t.Errorf("ledger/archived leaked into metrics: total=%d oldest=%q, want total=1 oldest=REAL",
			m.TotalUnresolved, m.OldestTicket)
	}
}

func TestComputeMetrics_EmptyUnresolvedSet(t *testing.T) {
	m := computeMetrics([]project.Attempt{mkMetricAttempt("D", 10*day, project.Done, 2*day)}, fixedNow)
	if m.OldestHasData {
		t.Errorf("Oldest must have no data when nothing is unresolved")
	}
	if m.TotalUnresolved != 0 {
		t.Errorf("TotalUnresolved = %d, want 0", m.TotalUnresolved)
	}
}

// TestComputeMetrics_ThroughputBoundary proves the 28-day boundary with a fixed clock:
// a ticket done 27 days ago counts, one done 29 days ago does not.
func TestComputeMetrics_ThroughputBoundary(t *testing.T) {
	attempts := []project.Attempt{
		mkMetricAttempt("JUST_IN", 40*day, project.Done, 27*day),  // done 27d ago → counts
		mkMetricAttempt("JUST_OUT", 40*day, project.Done, 29*day), // done 29d ago → excluded
		mkMetricAttempt("TODAY", 2*day, project.Done, 1*day),      // counts
	}
	m := computeMetrics(attempts, fixedNow)
	if m.Throughput4w != 2 {
		t.Errorf("Throughput4w = %d, want 2 (27d + 1d in window; 29d out)", m.Throughput4w)
	}
}

// TestComputeMetrics_DevCycleP85 checks the documented nearest-rank p85 over a known
// sample: dev-cycles of 1..10 days → ceil(0.85*10)=9 → the 9th-smallest = 9 days.
func TestComputeMetrics_DevCycleP85(t *testing.T) {
	var attempts []project.Attempt
	for i := 1; i <= 10; i++ {
		// created i days before done, done 1 day ago → dev-cycle = i days.
		a := project.Attempt{
			Ticket: "T", ID: "0001", Type: project.TypeTicket, State: project.Done,
			Events: []event.Event{
				{Seq: 1, Type: "created", TS: fixedNow.Add(-(time.Duration(i)*day + day))},
				{Seq: 2, Type: "done", TS: fixedNow.Add(-day)},
			},
		}
		attempts = append(attempts, a)
	}
	m := computeMetrics(attempts, fixedNow)
	if want := humanDuration(9 * day); m.ExpectedDevCycle != want {
		t.Errorf("ExpectedDevCycle = %q, want %q (p85 nearest-rank of 1..10 days)", m.ExpectedDevCycle, want)
	}
}

// TestComputeMetrics_DevCycleFloor shows an em dash below the 3-sample floor.
func TestComputeMetrics_DevCycleFloor(t *testing.T) {
	attempts := []project.Attempt{
		mkMetricAttempt("A", 5*day, project.Done, 1*day),
		mkMetricAttempt("B", 4*day, project.Done, 1*day),
	}
	m := computeMetrics(attempts, fixedNow)
	if m.ExpectedDevCycle != emDash {
		t.Errorf("ExpectedDevCycle = %q, want em dash below the %d-sample floor", m.ExpectedDevCycle, devCycleFloor)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	asc := []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := []struct {
		p    float64
		want time.Duration
	}{
		{0.85, 9}, // ceil(8.5)=9 → index 8
		{0.5, 5},  // ceil(5)=5 → index 4
		{1.0, 10}, // ceil(10)=10 → index 9
		{0.0, 1},  // clamps to rank 1 → index 0
	}
	for _, c := range cases {
		if got := percentileNearestRank(asc, c.p); got != c.want {
			t.Errorf("percentileNearestRank(p=%v) = %v, want %v", c.p, got, c.want)
		}
	}
}

// TestApplyBoardOrder_AgeSortWithinFavourites proves age-sort composes with the
// favourites partition: every favourite stays above every non-favourite, and each
// block is independently ordered oldest-created first.
func TestApplyBoardOrder_AgeSortWithinFavourites(t *testing.T) {
	// Running column, LoadAll (alphabetical) order A..F, with varied ages.
	mk := func(id string, ageDays int) project.Attempt {
		return project.Attempt{Ticket: id, ID: "0001", Type: project.TypeTicket,
			Events: []event.Event{{Seq: 1, Type: "created", TS: fixedNow.Add(-time.Duration(ageDays) * day)}}}
	}
	// Favourites: B (3d) and E (9d). Non-favourites: A (5d), C (1d), D (7d), F (2d).
	vm := boardVM{Running: []project.Attempt{mk("A", 5), mk("B", 3), mk("C", 1), mk("D", 7), mk("E", 9), mk("F", 2)}}
	faves := map[string]bool{"B/0001": true, "E/0001": true}

	applyBoardOrder(&vm, faves, true)

	var order []string
	for _, a := range vm.Running {
		order = append(order, a.Ticket)
	}
	// Favourites oldest-first among themselves: E (9d) then B (3d). Then non-favourites
	// oldest-first: D (7d), A (5d), F (2d), C (1d).
	want := []string{"E", "B", "D", "A", "F", "C"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("age-sort order = %v, want %v", order, want)
		}
	}
	if !vm.SortByAge {
		t.Error("applyBoardOrder should record SortByAge on the vm")
	}
	// The two favourites must both precede every non-favourite.
	for _, fav := range []string{"E", "B"} {
		for _, non := range []string{"D", "A", "F", "C"} {
			if indexOf(order, fav) > indexOf(order, non) {
				t.Errorf("favourite %s must sit above non-favourite %s", fav, non)
			}
		}
	}
}

// TestSortCookieRoundTrip checks parseSort/writeSortCookie agree on the on/off value.
func TestSortCookieRoundTrip(t *testing.T) {
	// ON writes draiver_sort=age; parseSort reads it back as true.
	rec := httptest.NewRecorder()
	writeSortCookie(rec, true)
	var val string
	var maxAge int
	for _, c := range rec.Result().Cookies() {
		if c.Name == sortCookie {
			val, maxAge = c.Value, c.MaxAge
		}
	}
	if val != sortAge || maxAge < 0 {
		t.Fatalf("ON cookie = %q (maxAge %d), want %q with a positive max-age", val, maxAge, sortAge)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: sortCookie, Value: sortAge})
	if !parseSort(r) {
		t.Error("parseSort should read the age cookie as ON")
	}
	// OFF expires the cookie (Max-Age<0) and parseSort of an absent cookie is false.
	recOff := httptest.NewRecorder()
	writeSortCookie(recOff, false)
	for _, c := range recOff.Result().Cookies() {
		if c.Name == sortCookie && c.MaxAge >= 0 {
			t.Errorf("OFF must expire the cookie, got max-age %d", c.MaxAge)
		}
	}
	if parseSort(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Error("parseSort with no cookie should be OFF")
	}
}

// TestSortToggleHandler drives POST /board/sort: the first toggle sets the age cookie
// and the Oldest tile renders pressed (aria-pressed="true"); resending that cookie
// toggles back off and clears it.
func TestSortToggleHandler(t *testing.T) {
	h := newServerOver(t, seedColumns(t))

	rr := post(t, h, "/board/sort")
	if rr.Code != 200 {
		t.Fatalf("POST /board/sort: status %d", rr.Code)
	}
	var sortVal string
	for _, c := range rr.Result().Cookies() {
		if c.Name == sortCookie {
			sortVal = c.Value
		}
	}
	if sortVal != sortAge {
		t.Fatalf("toggle must set %s=%s, got %q", sortCookie, sortAge, sortVal)
	}
	if !strings.Contains(rr.Body.String(), `data-testid="metric-oldest"`) {
		t.Errorf("sorted board should still render the Oldest tile:\n%s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `aria-pressed="true"`) {
		t.Errorf("with age-sort on, the Oldest tile must render aria-pressed=\"true\":\n%s", rr.Body.String())
	}

	// Resend the cookie: the second toggle clears it (back to the default order).
	r2 := httptest.NewRequest(http.MethodPost, "/board/sort", nil)
	r2.AddCookie(&http.Cookie{Name: sortCookie, Value: sortAge})
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, r2)
	cleared := false
	for _, c := range rr2.Result().Cookies() {
		if c.Name == sortCookie && (c.Value == "" || c.MaxAge < 0) {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("second toggle must clear the sort cookie")
	}
	if !strings.Contains(rr2.Body.String(), `aria-pressed="false"`) {
		t.Errorf("toggled off, the Oldest tile must render aria-pressed=\"false\"")
	}
}

// TestBoardStripRenders checks the four metric tiles ship inside the polled region.
func TestBoardStripRenders(t *testing.T) {
	h := newServerOver(t, seedColumns(t))
	body := get(t, h, "/board").Body.String()
	for _, id := range []string{
		"flow-metrics", "metric-oldest", "metric-devcycle-value",
		"metric-unresolved-value", "metric-throughput-value",
	} {
		if !strings.Contains(body, `data-testid="`+id+`"`) {
			t.Errorf("board region missing metric tile %q:\n%s", id, body)
		}
	}
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}
