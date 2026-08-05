package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func seedBoard(t *testing.T) store.Root {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}

	mk := func(id, title, att string) {
		if err := root.EnsureAttemptDirs(id, att); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(root.SpecPath(id), []byte("---\nid: "+id+"\ntitle: "+title+"\n---\n\n# "+title+"\n\nUse OAuth for login."), 0o644)
		if _, err := ticketlog.Append(root, id, att, event.Event{Type: "created", Actor: "a", Body: "start"}); err != nil {
			t.Fatal(err)
		}
	}

	// PROJ-1 attempt 0001 -> Needs me
	mk("PROJ-1", "Blocked one", "0001")
	ticketlog.Append(root, "PROJ-1", "0001", event.Event{Type: "escalation", Actor: "agent:x", Body: "which base image?"})
	// PROJ-1 attempt 0002 -> Running (same ticket, second card)
	root.EnsureAttemptDirs("PROJ-1", "0002")
	ticketlog.Append(root, "PROJ-1", "0002", event.Event{Type: "created", Actor: "a", Body: "retry"})

	// PROJ-2 attempt 0001 -> Review
	mk("PROJ-2", "Review two", "0001")
	ticketlog.Append(root, "PROJ-2", "0001", event.Event{Type: "review", Actor: "agent:x", Body: "done, please review"})

	// PROJ-3 attempt 0001 -> Running
	mk("PROJ-3", "Running three", "0001")
	return root
}

func newServer(t *testing.T) http.Handler {
	t.Helper()
	s, err := New(seedBoard(t))
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

func TestBoardShowsControlStatesPerAttempt(t *testing.T) {
	h := newServer(t)
	rr := get(t, h, "/")
	if rr.Code != 200 {
		t.Fatalf("GET / = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`data-testid="col-running"`,
		`data-testid="col-stuck"`,
		`data-testid="col-review"`,
		`data-testid="col-done"`,
		`data-testid="attempt-link-PROJ-1-0001"`, // blocked attempt in Stuck
		`data-testid="attempt-link-PROJ-1-0002"`, // same ticket, second card, Running
		"htmx.min.js",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("board missing %q", want)
		}
	}
}

func TestBoardPartialCounts(t *testing.T) {
	h := newServer(t)
	body := get(t, h, "/board").Body.String()
	// Stuck: PROJ-1/0001. Review: PROJ-2/0001. Running: PROJ-1/0002 + PROJ-3/0001.
	if !strings.Contains(body, `data-testid="count-stuck">1<`) {
		t.Errorf("stuck count wrong:\n%s", body)
	}
	if !strings.Contains(body, `data-testid="count-review">1<`) {
		t.Errorf("review count wrong")
	}
	if !strings.Contains(body, `data-testid="count-running">2<`) {
		t.Errorf("running count wrong (want 2):\n%s", body)
	}
}

// TestBoardCardsDeepLinkStuckAndReview pins that a Stuck or Review card links
// straight to the latest log entry (the open escalation / the review claim) via
// an #event-N fragment, while Running/Done cards link to the attempt with no
// fragment. One click lands the human on the exact entry needing attention.
func TestBoardCardsDeepLinkStuckAndReview(t *testing.T) {
	h := newServer(t)
	body := get(t, h, "/board").Body.String()
	for _, want := range []string{
		// Stuck: PROJ-1/0001's latest event is the escalation (#2).
		`data-testid="attempt-link-PROJ-1-0001" href="/ticket/PROJ-1/0001#event-2"`,
		// Review: PROJ-2/0001's latest event is the review claim (#2).
		`data-testid="attempt-link-PROJ-2-0001" href="/ticket/PROJ-2/0001#event-2"`,
		// Running cards stay plain (no deep link).
		`data-testid="attempt-link-PROJ-3-0001" href="/ticket/PROJ-3/0001"`,
		`data-testid="attempt-link-PROJ-1-0002" href="/ticket/PROJ-1/0002"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("board card link missing %q\n%s", want, body)
		}
	}
	// A Running/Done card must not carry an #event fragment.
	if strings.Contains(body, `href="/ticket/PROJ-3/0001#event`) {
		t.Errorf("Running card should not deep-link to a log entry")
	}
}

func TestAttemptIndexListsAttempts(t *testing.T) {
	h := newServer(t)
	rr := get(t, h, "/ticket/PROJ-1")
	if rr.Code != 200 {
		t.Fatalf("GET /ticket/PROJ-1 = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`data-testid="attempt-index"`,
		`href="/ticket/PROJ-1/0001"`,
		`href="/ticket/PROJ-1/0002"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("attempt index missing %q", want)
		}
	}
}

func TestAttemptDetailRendersSpecAndTimeline(t *testing.T) {
	h := newServer(t)
	rr := get(t, h, "/ticket/PROJ-1/0001")
	if rr.Code != 200 {
		t.Fatalf("GET /ticket/PROJ-1/0001 = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`data-testid="ticket-detail"`,
		`data-testid="state-badge"`,
		"Use OAuth for login.",              // rendered spec markdown
		`data-testid="event-1"`,             // created
		`data-testid="event-2"`,             // escalation
		"which base image?",                 // escalation body
		`data-testid="unresolved-2"`,        // shown as unresolved
		`data-testid="log-order-toggle"`,    // the ordering toggle
		`data-testid="log-timeline"`,        // the log list
		`data-testid="log-region"`,          // the htmx-polled log region
		`data-order="newest"`,               // default visual order is newest-first
		`hx-get="/ticket/PROJ-1/0001/live"`, // the region polls the live fragment
		`hx-trigger="every 3s"`,             // ...on the board's polling cadence
		"htmx.min.js",                       // htmx is loaded locally (no CDN)
		`data-testid="breadcrumb"`,          // board › attempts › <attempt> trail
		`href="/"`,                          // breadcrumb: one click to the board
		`href="/ticket/PROJ-1"`,             // breadcrumb: one click to the attempt list
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}

	// The DOM is oldest-first (#1 before #2); the newest-first *default* is a
	// visual CSS flip keyed off data-order on the region, so an htmx swap that
	// replaces only the list can never disturb the reader's chosen order.
	if i1, i2 := strings.Index(body, `data-testid="event-1"`), strings.Index(body, `data-testid="event-2"`); i1 > i2 {
		t.Errorf("expected oldest-first DOM: event-1 (%d) should precede event-2 (%d)", i1, i2)
	}
}

// TestTimelineShowsRelativeTimestamps pins the timestamp contract (drvweb-002):
// each log entry renders a relative age instead of the raw minute-precision
// stamp, carries the machine-readable datetime the client script reads to keep
// the age live, and surfaces the precise UTC timestamp as a hover tooltip. The
// page also loads reltime.js, which recomputes the age on load, after each log
// swap, and on a tick (so a non-polling Done page does not freeze at load age).
func TestTimelineShowsRelativeTimestamps(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("REL-1", "0001"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath("REL-1"), []byte("---\nid: REL-1\ntitle: Rel\n---\n\nspec"), 0o644)
	// A fixed age makes the relative bucket deterministic: three minutes back.
	ts := time.Now().Add(-3 * time.Minute)
	if _, err := ticketlog.Append(root, "REL-1", "0001", event.Event{Type: "created", Actor: "a", TS: ts, Body: "start"}); err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	body := get(t, h, "/ticket/REL-1/0001").Body.String()
	iso := ts.UTC().Format(time.RFC3339)
	full := ts.UTC().Format("2006-01-02 15:04:05 UTC")
	for _, want := range []string{
		`<time class="ts"`,       // the timestamp is a semantic <time> element...
		"3 minutes ago",          // ...showing a relative age, not the raw stamp
		`datetime="` + iso + `"`, // machine-readable stamp for the live client script
		`title="` + full + `"`,   // full, second-precision UTC timestamp on hover
		"/static/reltime.js",     // the script that keeps the age live
	} {
		if !strings.Contains(body, want) {
			t.Errorf("timeline missing %q\n%s", want, body)
		}
	}
	// The raw minute-precision stamp the page used to print must be gone.
	if raw := ts.UTC().Format("2006-01-02 15:04Z"); strings.Contains(body, raw) {
		t.Errorf("timeline still shows the raw minute-precision timestamp %q", raw)
	}
}

// TestDeepLinkAnchors pins the deep-link contract (task-011): every log event is
// URL-addressable, its seq label is a copyable permalink, and the anchor markup
// is present in BOTH the full page and the /live fragment — so navigating to
// /ticket/{id}/{attempt}#event-N lands on and highlights event N, and an htmx
// swap of the log region re-renders the same ids (the client script re-applies
// the highlight after the swap). The highlight itself is styled in style.css.
func TestDeepLinkAnchors(t *testing.T) {
	h := newServer(t)

	// Full page: each event is a scroll target, the seq is a permalink, and the
	// client script re-targets after every htmx settle so the highlight survives
	// the log poll (task-008's innerHTML swap drops the browser's :target ref).
	page := get(t, h, "/ticket/PROJ-1/0001").Body.String()
	for _, want := range []string{
		`id="event-1"`,     // event is a URL fragment target
		`id="event-2"`,     //
		`href="#event-1"`,  // seq label is a copyable permalink
		`href="#event-2"`,  //
		"htmx:afterSettle", // highlight is re-applied after each log swap
	} {
		if !strings.Contains(page, want) {
			t.Errorf("detail page missing deep-link hook %q", want)
		}
	}

	// The live fragment carries the same anchor ids, so a swap never strips the
	// addressable targets out of the DOM.
	frag := get(t, h, "/ticket/PROJ-1/0001/live").Body.String()
	for _, want := range []string{`id="event-1"`, `href="#event-1"`} {
		if !strings.Contains(frag, want) {
			t.Errorf("live fragment missing deep-link hook %q", want)
		}
	}

	// The stylesheet highlights the targeted entry (native :target on load and
	// the JS .is-target mirror across swaps).
	css := get(t, h, "/static/style.css")
	if css.Code != 200 {
		t.Fatalf("GET /static/style.css = %d", css.Code)
	}
	cssBody := css.Body.String()
	for _, want := range []string{".event:target", ".event.is-target"} {
		if !strings.Contains(cssBody, want) {
			t.Errorf("style.css missing target highlight rule %q", want)
		}
	}
}

// TestAttemptLiveFragment pins the htmx polling contract: GET .../live returns
// the log <ol> as the primary swap plus an out-of-band state badge (and count),
// so a single poll refreshes every live region without a full-page reload. It is
// a fragment, not a page: no <html>/<head> chrome.
func TestAttemptLiveFragment(t *testing.T) {
	h := newServer(t)
	rr := get(t, h, "/ticket/PROJ-1/0001/live")
	if rr.Code != 200 {
		t.Fatalf("GET .../live = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`data-testid="log-timeline"`, // primary swap: the log list
		`data-testid="event-1"`,      // ...with the events
		`id="state-badge"`,           // OOB state badge target
		`hx-swap-oob="true"`,         // ...swapped out of band
	} {
		if !strings.Contains(body, want) {
			t.Errorf("live fragment missing %q", want)
		}
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "<head") {
		t.Errorf("live fragment must not carry full-page chrome:\n%s", body)
	}
	// Oldest-first DOM in the fragment too, so swaps stay consistent with the page.
	if i1, i2 := strings.Index(body, `data-testid="event-1"`), strings.Index(body, `data-testid="event-2"`); i1 > i2 {
		t.Errorf("live fragment should be oldest-first DOM: event-1 (%d) before event-2 (%d)", i1, i2)
	}
}

// TestAttemptLiveStopsPollingWhenDone pins that a terminal (Done) attempt answers
// the live fragment with HTTP 286, htmx's signal to cancel the polling trigger,
// and that its page renders the log region without an hx-trigger so polling never
// starts.
func TestAttemptLiveStopsPollingWhenDone(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("DONE-1", "0001"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath("DONE-1"), []byte("---\nid: DONE-1\ntitle: Done\n---\n\nspec"), 0o644)
	if _, err := ticketlog.Append(root, "DONE-1", "0001", event.Event{Type: "created", Actor: "a", Body: "start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, "DONE-1", "0001", event.Event{Type: "done", Actor: "a", Body: "shipped"}); err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// The live fragment self-cancels with 286.
	if rr := get(t, h, "/ticket/DONE-1/0001/live"); rr.Code != 286 {
		t.Errorf("Done live fragment = %d, want 286 (stop polling)", rr.Code)
	}
	// The page never arms the log poll for a Done attempt. (The favicon poll is
	// board-state, which is never terminal, so it stays armed — see
	// TestBoardPollNeverQuiesces; only the immutable log region self-cancels.)
	page := get(t, h, "/ticket/DONE-1/0001").Body.String()
	if strings.Contains(page, `hx-get="/ticket/DONE-1/0001/live"`) {
		t.Errorf("Done attempt page must not arm the log poll")
	}
}

// TestBoardPollNeverQuiesces pins the counterpart decision to task-009: unlike a
// terminal Done attempt, the board is never terminal — new tickets/attempts can
// appear at any time and polling is the only thing that surfaces them (read-only,
// no server push). So even an empty/idle board must keep its 3s poll armed;
// quiescing it would strand the board until a manual reload.
func TestBoardPollNeverQuiesces(t *testing.T) {
	s, err := New(store.Root{Dir: t.TempDir()}) // no tickets: an idle board
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	page := get(t, h, "/").Body.String()
	for _, want := range []string{`hx-get="/board"`, `hx-trigger="every 3s"`} {
		if !strings.Contains(page, want) {
			t.Errorf("idle board must keep polling; missing %q", want)
		}
	}
}

// TestLogBodyMarkdownRenderedAndSanitized pins the contract that log-event
// bodies are treated as markdown on the board and rendered safely: structure
// (emphasis, code) becomes HTML, while raw HTML and dangerous link schemes an
// agent might write are neutralised. Log bodies are agent-authored input, so
// this guarantee must be a tested contract, not incidental goldmark behaviour.
func TestLogBodyMarkdownRenderedAndSanitized(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("MD-1", "0001"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath("MD-1"), []byte("---\nid: MD-1\ntitle: Markdown\n---\n\nspec"), 0o644)
	body := "Chose **server-side** paging; see `pager.go`. " +
		"<script>alert(1)</script> [x](javascript:alert(1))"
	if _, err := ticketlog.Append(root, "MD-1", "0001", event.Event{Type: "decision", Actor: "agent:x", Body: body}); err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	rr := get(t, s.Handler(), "/ticket/MD-1/0001")
	if rr.Code != 200 {
		t.Fatalf("GET detail = %d", rr.Code)
	}
	out := rr.Body.String()

	// Markdown structure is rendered to HTML.
	for _, want := range []string{"<strong>server-side</strong>", "<code>pager.go</code>"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected rendered markdown %q in body", want)
		}
	}
	// Dangerous input is neutralised: the raw <script> tag is dropped (goldmark
	// replaces it with a "raw HTML omitted" comment) and the javascript: link
	// destination is blanked. Check the exact payloads so the assertion can't
	// collide with the page's own legitimate <script> chrome.
	for _, bad := range []string{"<script>alert(1)", "javascript:alert(1)"} {
		if strings.Contains(out, bad) {
			t.Errorf("unsanitised %q leaked into rendered log body", bad)
		}
	}
}

// TestFaviconServedAndWired pins the favicon contract: all three SVG variants
// and the swap script are served from the embedded static FS, every full page
// references the favicon by the id the script swaps, and each badged variant
// carries its defining colour (eucalypt green "D" everywhere; rust-red only on
// Stuck; misty blue only on Review). Every full page — board and detail alike —
// server-renders the variant that matches live board state and is wired to
// receive updates. The per-refresh swap is driven by an HX-Trigger event, not
// scraping.
func TestFaviconServedAndWired(t *testing.T) {
	h := newServer(t)

	const green = "#3E6B48" // eucalypt "D"
	const rust = "#B7410E"  // Stuck badge
	const misty = "#6E9BB5" // Blue Mountains Review badge

	// Detail pages reflect live board state too: the seed has a Stuck attempt,
	// so they server-render the Stuck variant (same precedence as the board),
	// load the swap script, and poll /favicon-state for updates — no flash to
	// plain on load, and the icon tracks state changes happening elsewhere.
	for _, p := range []string{"/ticket/PROJ-1", "/ticket/PROJ-1/0001"} {
		page := get(t, h, p).Body.String()
		if !strings.Contains(page, `id="favicon"`) || !strings.Contains(page, `href="/static/favicon-stuck.svg"`) {
			t.Errorf("page %s should server-render the live Stuck favicon variant", p)
		}
		if !strings.Contains(page, `src="/static/favicon.js"`) {
			t.Errorf("page %s is missing the favicon swap script", p)
		}
		if !strings.Contains(page, `hx-get="/favicon-state"`) {
			t.Errorf("page %s is not wired to poll board favicon state", p)
		}
	}
	// The board page server-renders the badged variant matching the live board.
	// The seed has one Stuck attempt, so the favicon is correct on load with no
	// client-side scraping or plain→badged flash.
	boardPage := get(t, h, "/").Body.String()
	if !strings.Contains(boardPage, `id="favicon"`) || !strings.Contains(boardPage, `href="/static/favicon-stuck.svg"`) {
		t.Errorf("board page should server-render the Stuck favicon variant on load")
	}
	if !strings.Contains(boardPage, `src="/static/favicon.js"`) {
		t.Errorf("board page missing the favicon swap script")
	}

	// Each variant is served and carries exactly its defining colours: the green
	// D is on all three; each badge colour appears only on its own variant.
	for _, c := range []struct {
		path       string
		wantColors []string
		notColors  []string
	}{
		{"/static/favicon.svg", []string{green}, []string{rust, misty}},
		{"/static/favicon-stuck.svg", []string{green, rust}, []string{misty}},
		{"/static/favicon-review.svg", []string{green, misty}, []string{rust}},
	} {
		rr := get(t, h, c.path)
		if rr.Code != 200 {
			t.Fatalf("GET %s = %d", c.path, rr.Code)
		}
		body := rr.Body.String()
		for _, want := range c.wantColors {
			if !strings.Contains(body, want) {
				t.Errorf("%s should contain colour %s:\n%s", c.path, want, body)
			}
		}
		for _, bad := range c.notColors {
			if strings.Contains(body, bad) {
				t.Errorf("%s should not contain colour %s:\n%s", c.path, bad, body)
			}
		}
	}

	// The swap script listens for the server-emitted event and no longer scrapes
	// the board's data-testid counts.
	js := get(t, h, "/static/favicon.js")
	if js.Code != 200 {
		t.Fatalf("GET /static/favicon.js = %d", js.Code)
	}
	jsBody := js.Body.String()
	if !strings.Contains(jsBody, "draiver:favicon") {
		t.Errorf("favicon.js should listen for the draiver:favicon event")
	}
	for _, banned := range []string{"count-stuck", "count-review", "data-testid"} {
		if strings.Contains(jsBody, banned) {
			t.Errorf("favicon.js should not depend on the board test hook %q", banned)
		}
	}
}

// TestFaviconHrefPrecedence pins the Stuck>Review precedence that both the
// server-rendered initial href and the HX-Trigger event depend on: plain with
// nothing pending, Review badge with only reviews, Stuck badge whenever anything
// is blocked — even alongside reviews.
func TestFaviconHrefPrecedence(t *testing.T) {
	one := []project.Attempt{{}}
	for _, c := range []struct {
		name string
		vm   boardVM
		want string
	}{
		{"idle", boardVM{}, "/static/favicon.svg"},
		{"review only", boardVM{Review: one}, "/static/favicon-review.svg"},
		{"stuck only", boardVM{NeedsMe: one}, "/static/favicon-stuck.svg"},
		{"stuck outranks review", boardVM{NeedsMe: one, Review: one}, "/static/favicon-stuck.svg"},
	} {
		if got := c.vm.FaviconHref(); got != c.want {
			t.Errorf("%s: FaviconHref() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestBoardPartialEmitsFaviconTrigger pins that /board drives the favicon via an
// HX-Trigger response header (not DOM scraping), carrying the variant href.
func TestBoardPartialEmitsFaviconTrigger(t *testing.T) {
	h := newServer(t)
	trig := get(t, h, "/board").Header().Get("HX-Trigger")
	if trig == "" {
		t.Fatal("/board should emit an HX-Trigger favicon event")
	}
	// Seed has a Stuck attempt, and Stuck outranks Review.
	if !strings.Contains(trig, "draiver:favicon") || !strings.Contains(trig, "/static/favicon-stuck.svg") {
		t.Errorf("HX-Trigger = %q, want draiver:favicon → stuck variant", trig)
	}
}

// TestFaviconStateEmitsTrigger pins the detail pages' favicon channel: /favicon-state
// carries the same draiver:favicon HX-Trigger as /board (same precedence source)
// with an empty body, so a detail page's hidden poll updates the tab icon to
// live board state without pulling the board partial.
func TestFaviconStateEmitsTrigger(t *testing.T) {
	h := newServer(t)
	rr := get(t, h, "/favicon-state")
	if rr.Code != 200 {
		t.Fatalf("GET /favicon-state = %d", rr.Code)
	}
	if body := rr.Body.String(); body != "" {
		t.Errorf("/favicon-state body = %q, want empty (hx-swap=none)", body)
	}
	// Seed has a Stuck attempt, and Stuck outranks Review.
	if trig := rr.Header().Get("HX-Trigger"); !strings.Contains(trig, "draiver:favicon") || !strings.Contains(trig, "/static/favicon-stuck.svg") {
		t.Errorf("/favicon-state HX-Trigger = %q, want draiver:favicon → stuck variant", trig)
	}
}

func TestUnknownRoutes404(t *testing.T) {
	h := newServer(t)
	if rr := get(t, h, "/ticket/NOPE-1"); rr.Code != 404 {
		t.Errorf("unknown ticket = %d want 404", rr.Code)
	}
	if rr := get(t, h, "/ticket/PROJ-1/9999"); rr.Code != 404 {
		t.Errorf("unknown attempt = %d want 404", rr.Code)
	}
}

// TestRelativeAgeBuckets pins the wording and boundaries of the server-side
// "time ago" formatter across the ranges a real log spans. reltime.js mirrors
// these buckets on the client; keep the two in sync.
func TestRelativeAgeBuckets(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "just now"},
		{3 * time.Second, "just now"},
		{5 * time.Second, "5 seconds ago"},
		{59 * time.Second, "59 seconds ago"},
		{time.Minute, "1 minute ago"},
		{3 * time.Minute, "3 minutes ago"},
		{90 * time.Minute, "1 hour ago"},
		{5 * time.Hour, "5 hours ago"},
		{25 * time.Hour, "yesterday"},
		{3 * 24 * time.Hour, "3 days ago"},
		{10 * 24 * time.Hour, "1 week ago"},
		{40 * 24 * time.Hour, "1 month ago"},
		{400 * 24 * time.Hour, "1 year ago"},
		{-time.Minute, "just now"}, // future stamp (clock skew) clamps to now
	}
	for _, c := range cases {
		if got := relativeAge(c.d); got != c.want {
			t.Errorf("relativeAge(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestReadOnlyNoWriteRoutes(t *testing.T) {
	h := newServer(t)
	for _, path := range []string{"/", "/board", "/ticket/PROJ-1", "/ticket/PROJ-1/0001"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, expected 405 (read-only)", path, rr.Code)
		}
	}
}
