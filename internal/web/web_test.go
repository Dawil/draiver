package web

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// TestMain builds the real draiver binary once and points draiverBinOverride at
// it, so the web write tests drive the CLI end-to-end rather than a stub: the
// log append/decision/done tests exercise a real `draiver log`/`draiver done`,
// and the enable tests a real `ctl enable` appending to disk — genuine coverage
// of "the write goes through the draiver CLI" (decision #7). Under `go test`,
// os.Executable() is the test binary, not draiver, so the override is required.
func TestMain(m *testing.M) {
	os.Exit(func() int {
		dir, err := os.MkdirTemp("", "draiver-web-bin")
		if err != nil {
			fmt.Fprintln(os.Stderr, "tempdir for test binary:", err)
			return 1
		}
		defer os.RemoveAll(dir)
		bin := filepath.Join(dir, "draiver")
		build := exec.Command("go", "build", "-o", bin, "github.com/Dawil/draiver")
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "build draiver for tests:", err)
			return 1
		}
		draiverBinOverride = bin
		return m.Run()
	}())
}

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
		"Use OAuth for login.",                             // rendered spec markdown
		`data-testid="event-1"`,                            // created
		`data-testid="event-2"`,                            // escalation
		"which base image?",                                // escalation body
		`data-testid="unresolved-2"`,                       // shown as unresolved
		`data-testid="log-order-toggle"`,                   // the ordering toggle
		`data-testid="log-timeline"`,                       // the log list
		`data-testid="log-region"`,                         // the htmx-polled log region
		`data-order="newest"`,                              // default visual order is newest-first
		`hx-get="/ticket/PROJ-1/0001/live"`,                // the region polls the live fragment
		`hx-trigger="every 3s"`,                            // ...on the board's polling cadence
		"htmx.min.js",                                      // htmx is loaded locally (no CDN)
		`data-testid="breadcrumb"`,                         // board › attempts › <attempt> trail
		`href="/"`,                                         // breadcrumb: one click to the board
		`href="/ticket/PROJ-1"`,                            // breadcrumb: one click to the attempt list
		`data-testid="spec-toggle"`,                        // the spec more/less cap toggle
		`data-testid="agent-logs"`,                         // the Agent Logs section (between Spec and Log)
		`data-testid="agent-logs-stream"`,                  // the plain-text stream <pre>
		`data-stream-url="/ticket/PROJ-1/0001/agent-logs"`, // lazy SSE target
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}

	// Agent Logs sits between Spec and Log so the stream reads in page order.
	iSpec := strings.Index(body, `data-testid="spec"`)
	iAgent := strings.Index(body, `data-testid="agent-logs"`)
	iLog := strings.Index(body, `data-testid="log-region"`)
	if !(iSpec < iAgent && iAgent < iLog) {
		t.Errorf("Agent Logs should sit between Spec and Log (spec=%d agent=%d log=%d)", iSpec, iAgent, iLog)
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

	const green = Eucalypt      // eucalypt "D"
	const rust = MurrayRust     // Stuck badge
	const misty = BlueMountains // Blue Mountains Review badge

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

// TestEnableButtonAndPost pins the board's one write (drvweb-005): the green play
// button renders only on a Running, disabled card; POSTing the enable route
// appends exactly one `enable` event attributed to the server's actor and returns
// a card with the button gone; the write is idempotent (a re-POST is a no-op); a
// missing attempt 404s; and a cross-origin POST is blocked and writes nothing.
func TestEnableButtonAndPost(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// The button renders only for Running + disabled cards. The seed's Running
	// attempts (PROJ-3/0001, PROJ-1/0002) have no enable event, so both are
	// disabled and actionable; the Stuck and Review cards must not carry it.
	board := get(t, h, "/board").Body.String()
	for _, want := range []string{
		`data-testid="enable-btn-PROJ-3-0001"`,
		`data-testid="enable-btn-PROJ-1-0002"`,
		`aria-label="enable supervision"`, // an action with a label, not colour alone
	} {
		if !strings.Contains(board, want) {
			t.Errorf("board missing enable button %q", want)
		}
	}
	for _, notWant := range []string{
		`data-testid="enable-btn-PROJ-1-0001"`, // Stuck
		`data-testid="enable-btn-PROJ-2-0001"`, // Review
	} {
		if strings.Contains(board, notWant) {
			t.Errorf("enable button must not render for a non-Running card %q", notWant)
		}
	}

	// POST enables: exactly one enable event, attributed to the default actor, and
	// the returned card drops the button (htmx swaps it in place).
	rr := post(t, h, "/ticket/PROJ-3/0001/enable")
	if rr.Code != 200 {
		t.Fatalf("POST enable = %d, want 200", rr.Code)
	}
	if strings.Contains(rr.Body.String(), `data-testid="enable-btn-PROJ-3-0001"`) {
		t.Errorf("enabled card must no longer show the button:\n%s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `data-testid="card-PROJ-3-0001"`) {
		t.Errorf("enable response must be the re-rendered card:\n%s", rr.Body.String())
	}
	if got := enableEvents(t, root, "PROJ-3", "0001"); len(got) != 1 {
		t.Fatalf("want exactly 1 enable event, got %d", len(got))
	} else if got[0].Actor != "human:webui" {
		t.Errorf("enable actor = %q, want default human:webui", got[0].Actor)
	}

	// Idempotent: a re-POST (e.g. a double-click racing the board poll) writes
	// nothing new.
	if rr := post(t, h, "/ticket/PROJ-3/0001/enable"); rr.Code != 200 {
		t.Fatalf("re-POST enable = %d, want 200", rr.Code)
	}
	if n := len(enableEvents(t, root, "PROJ-3", "0001")); n != 1 {
		t.Errorf("re-POST must be a no-op; enable events = %d, want 1", n)
	}
	// ...and the now-enabled attempt shows no button on the next board render.
	if strings.Contains(get(t, h, "/board").Body.String(), `data-testid="enable-btn-PROJ-3-0001"`) {
		t.Errorf("an enabled attempt must not show the enable button")
	}

	// A non-existent attempt 404s and writes nothing.
	if rr := post(t, h, "/ticket/PROJ-1/9999/enable"); rr.Code != 404 {
		t.Errorf("POST enable to a missing attempt = %d, want 404", rr.Code)
	}

	// A cross-origin POST is blocked and appends nothing.
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ticket/PROJ-1/0002/enable", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d, want 403", rr.Code)
	}
	if n := len(enableEvents(t, root, "PROJ-1", "0002")); n != 0 {
		t.Errorf("a blocked POST must not write; enable events = %d, want 0", n)
	}
}

// TestEnableActorConfigurable pins that WithActor threads the write identity so a
// board-originated enable is attributable to the configured actor.
func TestEnableActorConfigurable(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root, WithActor("human:dave"))
	if err != nil {
		t.Fatal(err)
	}
	if rr := post(t, s.Handler(), "/ticket/PROJ-3/0001/enable"); rr.Code != 200 {
		t.Fatalf("POST enable = %d, want 200", rr.Code)
	}
	got := enableEvents(t, root, "PROJ-3", "0001")
	if len(got) != 1 || got[0].Actor != "human:dave" {
		t.Errorf("enable events = %+v, want one attributed to human:dave", got)
	}
}

// post issues a same-origin POST (no Sec-Fetch/Origin headers, as the test
// harness sends none — sameOrigin treats that as trusted).
func post(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, nil))
	return rr
}

// enableEvents returns an attempt's `enable` events, for asserting the write
// landed exactly once and carries the right actor.
func enableEvents(t *testing.T, root store.Root, id, att string) []event.Event {
	t.Helper()
	events, err := ticketlog.Read(root, id, att)
	if err != nil {
		t.Fatal(err)
	}
	var out []event.Event
	for _, e := range events {
		if e.Type == "enable" {
			out = append(out, e)
		}
	}
	return out
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

// TestReadOnlyNoWriteRoutes pins that the view routes stay GET-only: the sole
// write path is POST .../log (see the log-append tests), so posting to a page or
// the board is still a 405.
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

// seedReview writes a Review attempt (created + a review claim carrying links).
// ticketlog.Append deliberately does not validate links (that is the CLI layer),
// so a link with any scheme can be seeded here to exercise the render-time floor.
func seedReview(t *testing.T, root store.Root, id, att string, links []event.Link) {
	t.Helper()
	if err := root.EnsureAttemptDirs(id, att); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath(id), []byte("---\nid: "+id+"\ntitle: "+id+"\n---\n\nspec"), 0o644)
	if _, err := ticketlog.Append(root, id, att, event.Event{Type: "created", Actor: "a", Body: "start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, id, att, event.Event{Type: "review", Actor: "agent:x", Body: "done", Links: links}); err != nil {
		t.Fatal(err)
	}
}

func newServerOver(t *testing.T, root store.Root) http.Handler {
	t.Helper()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

// postLog posts a compose form to an attempt's log-append route. It sets a
// same-origin Origin header so the CSRF guard passes, mirroring a real htmx post.
func postLog(t *testing.T, h http.Handler, id, att, typ, body string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"type": {typ}, "body": {body}}
	req := httptest.NewRequest(http.MethodPost, "/ticket/"+id+"/"+att+"/log", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// stateOf reads an attempt's derived control state straight from the log, so a
// test asserts on the domain truth rather than the rendered badge.
func stateOf(t *testing.T, root store.Root, id, att string) project.State {
	t.Helper()
	a, err := project.LoadAttempt(root, id, att)
	if err != nil {
		t.Fatal(err)
	}
	return a.State
}

// TestComposeBoxAppendsTypedEntry pins the base compose contract: the detail page
// carries a compose control, and posting a curated type appends exactly one event
// of that type, returned in the live fragment without a full reload.
func TestComposeBoxAppendsTypedEntry(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// The control and its allow-listed options render on the detail page.
	page := get(t, h, "/ticket/PROJ-3/0001").Body.String()
	for _, want := range []string{
		`data-testid="log-compose"`,
		`data-testid="compose-type"`,
		`data-testid="compose-body"`,
		`data-testid="compose-submit"`,
		`value="note"`, `value="gotcha"`, `value="decision"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("detail page missing compose hook %q", want)
		}
	}

	before, _ := ticketlog.Read(root, "PROJ-3", "0001")
	rr := postLog(t, h, "PROJ-3", "0001", "gotcha", "the cache key ignores tenant")
	if rr.Code != 200 {
		t.Fatalf("POST log = %d, want 200\n%s", rr.Code, rr.Body.String())
	}
	after, _ := ticketlog.Read(root, "PROJ-3", "0001")
	if len(after) != len(before)+1 {
		t.Fatalf("expected exactly one new event, got %d -> %d", len(before), len(after))
	}
	last := after[len(after)-1]
	if last.Type != "gotcha" || last.Body != "the cache key ignores tenant" {
		t.Errorf("appended event = %+v, want gotcha with the posted body", last)
	}
	// The response is the live fragment: the new entry plus the OOB badge/count.
	out := rr.Body.String()
	for _, want := range []string{"the cache key ignores tenant", `id="state-badge"`, `hx-swap-oob="true"`} {
		if !strings.Contains(out, want) {
			t.Errorf("live fragment missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "<html") {
		t.Errorf("log-append response must be a fragment, not a full page")
	}
}

// TestComposeAppendStampsActor pins that a web-composed entry is attributable:
// the server stamps its resolved actor identity, not an empty one.
func TestComposeAppendStampsActor(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if rr := postLog(t, s.Handler(), "PROJ-3", "0001", "note", "context"); rr.Code != 200 {
		t.Fatalf("POST log = %d", rr.Code)
	}
	events, _ := ticketlog.Read(root, "PROJ-3", "0001")
	last := events[len(events)-1]
	if last.Actor == "" || !strings.HasPrefix(last.Actor, "human:") {
		t.Errorf("web-composed event actor = %q, want a resolved human: identity", last.Actor)
	}
}

// TestReviewDecisionReopensToRunning pins the first-class Review → Running verb:
// posting a decision on a Review attempt appends it and the derived state falls
// back to Running (Derive: a decision is the latest lifecycle marker).
func TestReviewDecisionReopensToRunning(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, root, "PROJ-2", "0001"); got != project.Review {
		t.Fatalf("seed PROJ-2/0001 = %q, want Review", got)
	}
	rr := postLog(t, s.Handler(), "PROJ-2", "0001", "decision", "spec was misread; more work needed")
	if rr.Code != 200 {
		t.Fatalf("POST decision = %d", rr.Code)
	}
	if got := stateOf(t, root, "PROJ-2", "0001"); got != project.Running {
		t.Errorf("after decision, state = %q, want Running (reopened)", got)
	}
	// The OOB badge in the response reflects the reopened state.
	if !strings.Contains(rr.Body.String(), "state-Running") {
		t.Errorf("live fragment badge should flip to Running\n%s", rr.Body.String())
	}
}

// TestReviewDoneClosesAttempt pins the terminal Review verb: posting done appends
// it and the attempt moves to Done (and its live poll then self-cancels at 286).
func TestReviewDoneClosesAttempt(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	if rr := postLog(t, h, "PROJ-2", "0001", "done", "verified, shipping"); rr.Code != 200 {
		t.Fatalf("POST done = %d", rr.Code)
	}
	if got := stateOf(t, root, "PROJ-2", "0001"); got != project.Done {
		t.Errorf("after done, state = %q, want Done", got)
	}
	// The now-terminal attempt's live fragment self-cancels the poll with 286.
	if rr := get(t, h, "/ticket/PROJ-2/0001/live"); rr.Code != 286 {
		t.Errorf("Done attempt live = %d, want 286 (stop polling)", rr.Code)
	}
}

// TestComposeRejectsOutOfSetType pins the allow-list: a lifecycle type that owns
// its own flow (escalation/resolution/review/enable/disable) cannot be smuggled
// in through the generic compose route, and nothing is appended.
func TestComposeRejectsOutOfSetType(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for _, typ := range []string{"escalation", "resolution", "review", "enable", "disable", "created", "bogus"} {
		before, _ := ticketlog.Read(root, "PROJ-3", "0001")
		rr := postLog(t, h, "PROJ-3", "0001", typ, "should not land")
		if rr.Code != http.StatusBadRequest {
			t.Errorf("POST type=%q = %d, want 400", typ, rr.Code)
		}
		after, _ := ticketlog.Read(root, "PROJ-3", "0001")
		if len(after) != len(before) {
			t.Errorf("type=%q was rejected but still appended an event", typ)
		}
	}
}

// TestReviewActionsRenderOnlyInReview pins that the Decision/Done buttons appear
// exactly when the attempt is in Review and are absent otherwise.
func TestReviewActionsRenderOnlyInReview(t *testing.T) {
	h := newServer(t)
	// PROJ-2/0001 is Review: both actions present.
	review := get(t, h, "/ticket/PROJ-2/0001").Body.String()
	for _, want := range []string{`data-testid="review-actions"`, `data-testid="action-decision"`, `data-testid="action-done"`} {
		if !strings.Contains(review, want) {
			t.Errorf("Review attempt missing %q", want)
		}
	}
	// PROJ-3/0001 is Running: no review actions.
	running := get(t, h, "/ticket/PROJ-3/0001").Body.String()
	for _, absent := range []string{`data-testid="review-actions"`, `data-testid="action-decision"`, `data-testid="action-done"`} {
		if strings.Contains(running, absent) {
			t.Errorf("non-Review attempt should not render %q", absent)
		}
	}
}

// TestLogAppendRejectsCrossOrigin pins the CSRF guard: a POST whose Origin names
// a different host is refused with 403 and nothing is appended.
func TestLogAppendRejectsCrossOrigin(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := ticketlog.Read(root, "PROJ-3", "0001")
	form := url.Values{"type": {"note"}, "body": {"forged"}}
	req := httptest.NewRequest(http.MethodPost, "/ticket/PROJ-3/0001/log", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d, want 403", rr.Code)
	}
	after, _ := ticketlog.Read(root, "PROJ-3", "0001")
	if len(after) != len(before) {
		t.Errorf("cross-origin POST must not append an event")
	}
}

// TestLogAppendUnknownAttempt404 pins that posting to a non-existent attempt is a
// 404, not a silent no-op or a 500.
func TestLogAppendUnknownAttempt404(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if rr := postLog(t, s.Handler(), "PROJ-3", "9999", "note", "x"); rr.Code != 404 {
		t.Errorf("POST to unknown attempt = %d, want 404", rr.Code)
	}
}

// TestReviewCardSurfacesPrimaryReviewLink pins that a Review card with links shows
// the external "Review changes" action pointing at the primary (rel pr/mr) link —
// not merely the first — opening in a new tab with rel="noopener noreferrer", and
// that the internal deep-link stays alongside it as a separate affordance.
func TestReviewCardSurfacesPrimaryReviewLink(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	// Links out of primary order: a diff first, the PR second. The primary must be
	// the pr/mr link regardless of position.
	seedReview(t, root, "REV-1", "0001", []event.Link{
		{Rel: "diff", Href: "https://example.com/diff/9"},
		{Rel: "pr", Href: "https://example.com/pr/1"},
	})
	body := get(t, newServerOver(t, root), "/board").Body.String()
	want := `data-testid="review-link-REV-1-0001" href="https://example.com/pr/1" target="_blank" rel="noopener noreferrer"`
	if !strings.Contains(body, want) {
		t.Errorf("review card action missing/mismatched:\nwant %q\n%s", want, body)
	}
	if !strings.Contains(body, "Review changes") {
		t.Errorf("review action label 'Review changes' missing")
	}
	// The internal deep-link is unchanged and separate from the external action.
	if !strings.Contains(body, `data-testid="attempt-link-REV-1-0001" href="/ticket/REV-1/0001#event-2"`) {
		t.Errorf("internal deep-link should remain alongside the external action:\n%s", body)
	}
}

// TestReviewCardNoLinkNoButton pins that a Review attempt with no links renders no
// external action (the direct-merge flow degrades to nothing).
func TestReviewCardNoLinkNoButton(t *testing.T) {
	// seedBoard's Review attempt (PROJ-2/0001) carries no links.
	body := get(t, newServer(t), "/board").Body.String()
	if strings.Contains(body, `data-testid="review-link-PROJ-2-0001"`) {
		t.Errorf("link-less Review card should render no action button:\n%s", body)
	}
	if strings.Contains(body, "Review changes") {
		t.Errorf("no review link -> no 'Review changes' action")
	}
}

// TestReviewLinkScopedToReviewColumn pins that the card action is scoped to the
// Review column: an attempt reopened to Running (a decision after a review) keeps
// the review event's link on its timeline chips but shows no stale card action.
func TestReviewLinkScopedToReviewColumn(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("REO-1", "0001"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath("REO-1"), []byte("---\nid: REO-1\ntitle: Reopened\n---\n\nspec"), 0o644)
	ticketlog.Append(root, "REO-1", "0001", event.Event{Type: "created", Actor: "a", Body: "start"})
	ticketlog.Append(root, "REO-1", "0001", event.Event{Type: "review", Actor: "agent:x", Body: "done",
		Links: []event.Link{{Rel: "pr", Href: "https://example.com/pr/9"}}})
	// A decision after the review reopens the attempt (Review -> Running).
	ticketlog.Append(root, "REO-1", "0001", event.Event{Type: "decision", Actor: "human:d", Body: "reopening: missing tests"})
	h := newServerOver(t, root)

	if board := get(t, h, "/board").Body.String(); strings.Contains(board, `data-testid="review-link-REO-1-0001"`) {
		t.Errorf("card action must be scoped to Review; a reopened (Running) card showed it:\n%s", board)
	}
	// The owning event's chips still render on the detail timeline regardless of state.
	if detail := get(t, h, "/ticket/REO-1/0001").Body.String(); !strings.Contains(detail, `data-testid="event-links-2"`) {
		t.Errorf("detail timeline should still render the review event's chips:\n%s", detail)
	}
}

// TestDetailTimelineRendersLinkChips pins that an event's links render as chips
// below the body, labelled by rel, each opening in a new tab safely.
func TestDetailTimelineRendersLinkChips(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	seedReview(t, root, "REV-2", "0001", []event.Link{
		{Rel: "pr", Href: "https://example.com/pr/2"},
		{Rel: "ci", Href: "https://ci.example.com/run/7"},
	})
	body := get(t, newServerOver(t, root), "/ticket/REV-2/0001").Body.String()
	for _, want := range []string{
		`data-testid="event-links-2"`,
		`href="https://example.com/pr/2" target="_blank" rel="noopener noreferrer"`,
		`href="https://ci.example.com/run/7" target="_blank" rel="noopener noreferrer"`,
		`>pr ↗</a>`,
		`>ci ↗</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail chip missing %q\n%s", want, body)
		}
	}
}

// TestRenderTimeSchemeRecheckDropsBadLinks pins the defense-in-depth floor: a link
// whose scheme is disallowed (seeded past append-time validation) becomes neither
// a card action nor a chip, and its dangerous href never reaches a served page.
func TestRenderTimeSchemeRecheckDropsBadLinks(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	seedReview(t, root, "BAD-1", "0001", []event.Link{
		{Rel: "pr", Href: "javascript:alert(1)"},
		{Rel: "diff", Href: "data:text/html,<script>alert(1)</script>"},
	})
	h := newServerOver(t, root)
	boardOut := get(t, h, "/board").Body.String()
	detailOut := get(t, h, "/ticket/BAD-1/0001").Body.String()

	if strings.Contains(boardOut, `data-testid="review-link-BAD-1-0001"`) {
		t.Errorf("bad-scheme link must not become a card action:\n%s", boardOut)
	}
	if strings.Contains(detailOut, `data-testid="event-links-2"`) {
		t.Errorf("bad-scheme links must not render chips:\n%s", detailOut)
	}
	for _, bad := range []string{"javascript:alert(1)", "data:text/html"} {
		if strings.Contains(boardOut, bad) || strings.Contains(detailOut, bad) {
			t.Errorf("dangerous href %q leaked into a served page", bad)
		}
	}
}

// TestBodyLinkSchemesSanitizedAtRender pins the goldmark URL policy (linkPolicy):
// an allowed https body link renders live, while javascript:, non-image data:,
// and — the gap goldmark's default admits — data:image/* links and autolinks
// never produce a live href.
func TestBodyLinkSchemesSanitizedAtRender(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("MDX-1", "0001"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath("MDX-1"), []byte("---\nid: MDX-1\ntitle: t\n---\n\nspec"), 0o644)
	body := "js [a](javascript:alert(1)) data [b](data:text/html,x) " +
		"img [c](data:image/png;base64,AAAA) auto <data:image/png;base64,BBBB> " +
		"ok [d](https://example.com/pr/1)"
	ticketlog.Append(root, "MDX-1", "0001", event.Event{Type: "note", Actor: "a", Body: body})
	out := get(t, newServerOver(t, root), "/ticket/MDX-1/0001").Body.String()

	if !strings.Contains(out, `href="https://example.com/pr/1"`) {
		t.Errorf("allowed https body link should render live:\n%s", out)
	}
	for _, bad := range []string{`href="javascript:`, `href="data:text/html`, `href="data:image/png`} {
		if strings.Contains(out, bad) {
			t.Errorf("disallowed scheme produced a live href %q:\n%s", bad, out)
		}
	}
}

// --- inline escalation resolution (POST /ticket/{id}/{attempt}/resolve) ---

// seedEscalation builds one attempt in a fresh root: `created` (#1) then one open
// `escalation` (#2), so the attempt derives Stuck (NeedsMe). It is the fixture for
// the resolve tests; extra escalations/resolutions are appended per test.
func seedEscalation(t *testing.T) store.Root {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}
	if err := root.EnsureAttemptDirs("ESC-1", "0001"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(root.SpecPath("ESC-1"), []byte("---\nid: ESC-1\ntitle: Escalated\n---\n\nspec"), 0o644)
	if _, err := ticketlog.Append(root, "ESC-1", "0001", event.Event{Type: "created", Actor: "a", Body: "start"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, "ESC-1", "0001", event.Event{Type: "escalation", Actor: "agent:x", Body: "which base image?"}); err != nil {
		t.Fatal(err)
	}
	return root
}

// postResolve posts the resolve form for one escalation. It sets a same-origin
// Origin header so the CSRF guard passes, mirroring a real htmx post.
func postResolve(t *testing.T, h http.Handler, id, att string, seq int, answer string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"seq": {strconv.Itoa(seq)}, "answer": {answer}}
	req := httptest.NewRequest(http.MethodPost, "/ticket/"+id+"/"+att+"/resolve", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestResolveBoxRendersForOpenEscalationOnly pins the render contract: an open
// escalation carries a resolution box (with the hx-preserve'd textarea so the 3s
// poll never clobbers a half-typed answer), and once resolved it shows only the
// "resolved by #N" note with no box.
func TestResolveBoxRendersForOpenEscalationOnly(t *testing.T) {
	root := seedEscalation(t)
	h := newServerOver(t, root)

	page := get(t, h, "/ticket/ESC-1/0001").Body.String()
	for _, want := range []string{
		`data-testid="resolve-form-2"`,
		`data-testid="resolve-input-2"`,
		`id="resolve-input-2"`,
		`hx-preserve="true"`,
		`data-testid="resolve-submit-2"`,
		"/ticket/ESC-1/0001/resolve",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("open escalation detail missing %q", want)
		}
	}

	// Answer it out of band; the box must disappear, leaving only the resolved note.
	if _, err := ticketlog.Append(root, "ESC-1", "0001", event.Event{Type: "resolution", Actor: "human:d", Refs: []int{2}, Body: "use alpine"}); err != nil {
		t.Fatal(err)
	}
	page = get(t, h, "/ticket/ESC-1/0001").Body.String()
	if !strings.Contains(page, `data-testid="resolved-2"`) {
		t.Errorf("resolved escalation should show the resolved note")
	}
	if strings.Contains(page, `data-testid="resolve-form-2"`) {
		t.Errorf("resolved escalation must not render a resolve box")
	}
}

// TestResolveInputKeepsFocusAcrossPoll pins the focus-retention contract
// (drvweb-014): the detail page ships a client handler that snapshots the
// focused resolve textarea before the 3s log-region swap and refocuses it (with
// caret) right after, so hx-preserve's value-retention is joined by
// focus-retention and the poll no longer steals the cursor mid-sentence. The
// script is inline in the page, so we assert its keying + call shape rather than
// behaviour.
func TestResolveInputKeepsFocusAcrossPoll(t *testing.T) {
	root := seedEscalation(t)
	h := newServerOver(t, root)

	page := get(t, h, "/ticket/ESC-1/0001").Body.String()
	for _, want := range []string{
		"htmx:beforeSwap",   // snapshot focus before the region is swapped
		"htmx:afterSwap",    // restore it once the new content is in place
		"preventScroll",     // refocus must not yank the reader
		"setSelectionRange", // ...and it restores the caret/selection, not just focus
	} {
		if !strings.Contains(page, want) {
			t.Errorf("detail page missing resolve-focus handler marker %q", want)
		}
	}
}

// TestResolvePostClearsStuck pins the happy path: posting an answer appends
// exactly one resolution refing that escalation's seq, attributable to a human,
// and the attempt leaves Stuck for Running in the same live fragment.
func TestResolvePostClearsStuck(t *testing.T) {
	root := seedEscalation(t)
	h := newServerOver(t, root)
	if got := stateOf(t, root, "ESC-1", "0001"); got != project.NeedsMe {
		t.Fatalf("seed state = %q, want NeedsMe (Stuck)", got)
	}

	before, _ := ticketlog.Read(root, "ESC-1", "0001")
	rr := postResolve(t, h, "ESC-1", "0001", 2, "use the alpine base image")
	if rr.Code != 200 {
		t.Fatalf("POST resolve = %d, want 200\n%s", rr.Code, rr.Body.String())
	}

	after, _ := ticketlog.Read(root, "ESC-1", "0001")
	if len(after) != len(before)+1 {
		t.Fatalf("expected exactly one new event, got %d -> %d", len(before), len(after))
	}
	last := after[len(after)-1]
	if last.Type != "resolution" {
		t.Errorf("appended event type = %q, want resolution", last.Type)
	}
	if len(last.Refs) != 1 || last.Refs[0] != 2 {
		t.Errorf("resolution refs = %v, want [2]", last.Refs)
	}
	if last.Body != "use the alpine base image" {
		t.Errorf("resolution body = %q, want the posted answer", last.Body)
	}
	if !strings.HasPrefix(last.Actor, "human:") {
		t.Errorf("resolution actor = %q, want a resolved human: identity", last.Actor)
	}
	if got := stateOf(t, root, "ESC-1", "0001"); got != project.Running {
		t.Errorf("after resolve, state = %q, want Running", got)
	}

	out := rr.Body.String()
	if !strings.Contains(out, "state-Running") {
		t.Errorf("live fragment badge should leave Stuck for Running\n%s", out)
	}
	if !strings.Contains(out, `data-testid="resolved-2"`) {
		t.Errorf("live fragment should flip the escalation to resolved\n%s", out)
	}
	if strings.Contains(out, `data-testid="resolve-form-2"`) {
		t.Errorf("live fragment should no longer render the resolve box")
	}
	if strings.Contains(out, "<html") {
		t.Errorf("resolve response must be a fragment, not a full page")
	}
}

// TestResolvePostRejectsBadSeq pins the server-side guards, matching the CLI and
// closing the gap it leaves: an unknown seq, a non-escalation seq, an
// already-resolved seq, and an empty answer are all rejected and none append.
func TestResolvePostRejectsBadSeq(t *testing.T) {
	root := seedEscalation(t)
	h := newServerOver(t, root)
	// Pre-resolve #2 so the already-resolved path has a target.
	if _, err := ticketlog.Append(root, "ESC-1", "0001", event.Event{Type: "escalation", Actor: "agent:x", Body: "second question?"}); err != nil {
		t.Fatal(err) // #3, left open
	}
	if _, err := ticketlog.Append(root, "ESC-1", "0001", event.Event{Type: "resolution", Actor: "human:d", Refs: []int{2}, Body: "answered #2"}); err != nil {
		t.Fatal(err) // #4 resolves #2
	}

	cases := []struct {
		name   string
		seq    int
		answer string
	}{
		{"unknown seq", 99, "no such event"},
		{"non-escalation seq", 1, "the created event is not an escalation"},
		{"already-resolved seq", 2, "answered twice"},
		{"empty answer", 3, "   "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before, _ := ticketlog.Read(root, "ESC-1", "0001")
			rr := postResolve(t, h, "ESC-1", "0001", c.seq, c.answer)
			if rr.Code < 400 || rr.Code >= 500 {
				t.Errorf("POST resolve %s = %d, want a 4xx rejection\n%s", c.name, rr.Code, rr.Body.String())
			}
			after, _ := ticketlog.Read(root, "ESC-1", "0001")
			if len(after) != len(before) {
				t.Errorf("%s must not append an event (%d -> %d)", c.name, len(before), len(after))
			}
		})
	}
}

// TestResolveTwoOpenEscalationsIndependent pins that with two open escalations,
// each box answers exactly its own seq: resolving one leaves the other open (the
// attempt stays Stuck) and only clears once both are answered.
func TestResolveTwoOpenEscalationsIndependent(t *testing.T) {
	root := seedEscalation(t)
	// A second open escalation (#3) alongside #2.
	if _, err := ticketlog.Append(root, "ESC-1", "0001", event.Event{Type: "escalation", Actor: "agent:x", Body: "and the DB url?"}); err != nil {
		t.Fatal(err)
	}
	h := newServerOver(t, root)

	// Both boxes render.
	page := get(t, h, "/ticket/ESC-1/0001").Body.String()
	for _, want := range []string{`data-testid="resolve-form-2"`, `data-testid="resolve-form-3"`} {
		if !strings.Contains(page, want) {
			t.Errorf("detail with two open escalations missing %q", want)
		}
	}

	// Resolve #2 only: #3 stays open, attempt stays Stuck.
	rr := postResolve(t, h, "ESC-1", "0001", 2, "alpine")
	if rr.Code != 200 {
		t.Fatalf("POST resolve #2 = %d\n%s", rr.Code, rr.Body.String())
	}
	if got := stateOf(t, root, "ESC-1", "0001"); got != project.NeedsMe {
		t.Errorf("after resolving one of two, state = %q, want NeedsMe (still Stuck)", got)
	}
	out := rr.Body.String()
	if !strings.Contains(out, `data-testid="resolved-2"`) {
		t.Errorf("escalation #2 should be resolved\n%s", out)
	}
	if !strings.Contains(out, `data-testid="resolve-form-3"`) {
		t.Errorf("escalation #3 should still carry its own box\n%s", out)
	}
	if strings.Contains(out, `data-testid="resolve-form-2"`) {
		t.Errorf("escalation #2's box should be gone")
	}

	// Resolve #3: now the attempt clears.
	if rr := postResolve(t, h, "ESC-1", "0001", 3, "from a fixture"); rr.Code != 200 {
		t.Fatalf("POST resolve #3 = %d\n%s", rr.Code, rr.Body.String())
	}
	if got := stateOf(t, root, "ESC-1", "0001"); got != project.Running {
		t.Errorf("after resolving both, state = %q, want Running", got)
	}
}

// TestResolveCrossOriginRejected pins the CSRF guard on the resolve write: a POST
// whose Origin names a different host is refused 403 and nothing is appended.
func TestResolveCrossOriginRejected(t *testing.T) {
	root := seedEscalation(t)
	h := newServerOver(t, root)
	before, _ := ticketlog.Read(root, "ESC-1", "0001")
	form := url.Values{"seq": {"2"}, "answer": {"forged"}}
	req := httptest.NewRequest(http.MethodPost, "/ticket/ESC-1/0001/resolve", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-origin resolve = %d, want 403", rr.Code)
	}
	after, _ := ticketlog.Read(root, "ESC-1", "0001")
	if len(after) != len(before) {
		t.Errorf("cross-origin resolve must not append an event")
	}
}

// TestAgentLogsStreamsSessionAsSSE pins the drvweb-011 stream: GET
// /ticket/{id}/{attempt}/agent-logs returns text/event-stream and relays the
// attempt's session logs — the same content `ctl logs -f` prints — one rendered
// line per `data:` event. It seeds a recorded assistant line and asserts it
// arrives over the stream, exercising the real shell-out to the CLI end-to-end
// (draiverBinOverride points at the freshly built binary, like the write tests).
func TestAgentLogsStreamsSessionAsSSE(t *testing.T) {
	root := seedBoard(t)
	if err := root.EnsureSessionDir("PROJ-3", "0001"); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"text","text":"working on it"}]}}` + "\n"
	if err := os.WriteFile(root.SessionStreamPath("PROJ-3", "0001"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// Follow mode never ends on its own; the timeout is the safety net that fails
	// the test (and tears down the child) if the seeded line never arrives.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/ticket/PROJ-3/0001/agent-logs", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	sc := bufio.NewScanner(resp.Body)
	found := false
	for sc.Scan() {
		txt := sc.Text()
		if strings.HasPrefix(txt, "data:") && strings.Contains(txt, "working on it") {
			found = true
			break
		}
	}
	cancel() // stop following; unblocks the reader and kills the child
	if !found {
		t.Fatalf("did not receive the seeded session line as an SSE data event")
	}
}

// TestAgentLogsUnknownAttempt404s pins that the stream endpoint 404s an attempt
// that does not exist rather than spawning a follow against a missing session.
func TestAgentLogsUnknownAttempt404s(t *testing.T) {
	h := newServer(t)
	rr := get(t, h, "/ticket/PROJ-3/9999/agent-logs")
	if rr.Code != http.StatusNotFound {
		t.Errorf("agent-logs for unknown attempt = %d, want 404", rr.Code)
	}
}
