package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"draiver/internal/event"
	"draiver/internal/store"
	"draiver/internal/ticketlog"
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
		"Use OAuth for login.",       // rendered spec markdown
		`data-testid="event-1"`,           // created
		`data-testid="event-2"`,           // escalation
		"which base image?",               // escalation body
		`data-testid="unresolved-2"`,      // shown as unresolved
		`data-testid="log-order-toggle"`,  // the ordering toggle
		`data-testid="log-timeline"`,      // the toggle's target list
		`data-order="newest"`,             // default order is newest-first
		`data-testid="breadcrumb"`,        // board › attempts › <attempt> trail
		`href="/"`,                        // breadcrumb: one click to the board
		`href="/ticket/PROJ-1"`,           // breadcrumb: one click to the attempt list
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}

	// Default ordering is newest-first: the later event (#2) renders before #1.
	if i2, i1 := strings.Index(body, `data-testid="event-2"`), strings.Index(body, `data-testid="event-1"`); i2 > i1 {
		t.Errorf("expected newest-first: event-2 (%d) should precede event-1 (%d)", i2, i1)
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

func TestUnknownRoutes404(t *testing.T) {
	h := newServer(t)
	if rr := get(t, h, "/ticket/NOPE-1"); rr.Code != 404 {
		t.Errorf("unknown ticket = %d want 404", rr.Code)
	}
	if rr := get(t, h, "/ticket/PROJ-1/9999"); rr.Code != 404 {
		t.Errorf("unknown attempt = %d want 404", rr.Code)
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
