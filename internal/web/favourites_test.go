package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// --- cookie parse/encode (drv-050) ---

func reqWithFaves(cookie string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: favesCookie, Value: cookie})
	}
	return r
}

func TestParseFavesMissingAndValid(t *testing.T) {
	// No cookie → empty set, never nil, no error.
	if got := parseFaves(reqWithFaves("")); len(got) != 0 {
		t.Errorf("missing cookie should parse to empty set, got %v", got)
	}
	// A valid comma-joined, percent-encoded set round-trips.
	val := url.QueryEscape("drv-050/0001") + "," + url.QueryEscape("RUN-1/0002")
	got := parseFaves(reqWithFaves(val))
	if !got["drv-050/0001"] || !got["RUN-1/0002"] || len(got) != 2 {
		t.Errorf("valid cookie parsed wrong: %v", got)
	}
}

func TestParseFavesDropsMalformed(t *testing.T) {
	// A mix of a valid ref, a token with a disallowed char, a token with no slash, a
	// token with two slashes, and an empty token. Only the valid ref survives — a
	// stale/malformed cookie must never error the render, just drop the bad tokens.
	val := strings.Join([]string{
		url.QueryEscape("drv-050/0001"), // good
		url.QueryEscape("bad ref/0001"), // space → dropped
		url.QueryEscape("noslash"),      // no slash → dropped
		url.QueryEscape("a/b/c"),        // two slashes → dropped
		"",                              // empty → skipped
		"%zz",                           // undecodable → dropped
	}, ",")
	got := parseFaves(reqWithFaves(val))
	if len(got) != 1 || !got["drv-050/0001"] {
		t.Errorf("only the well-formed ref should survive, got %v", got)
	}
}

func TestParseFavesCaps(t *testing.T) {
	// An oversized cookie is truncated, never errored: build way past both caps.
	var toks []string
	for i := 0; i < favesMaxRefs+500; i++ {
		toks = append(toks, url.QueryEscape("drv-9/"+strings.Repeat("0", 3)+string(rune('a'+i%26))))
	}
	got := parseFaves(reqWithFaves(strings.Join(toks, ",")))
	if len(got) > favesMaxRefs {
		t.Errorf("parseFaves must cap at %d refs, got %d", favesMaxRefs, len(got))
	}
}

func TestEncodeFavesStableAndSafe(t *testing.T) {
	set := map[string]bool{"drv-050/0001": true, "ACC-1/0002": true}
	a := encodeFaves(set)
	b := encodeFaves(set)
	if a != b {
		t.Errorf("encodeFaves must be stable: %q != %q", a, b)
	}
	// Sorted: ACC-1 before drv-050 (byte order, uppercase first).
	if !strings.HasPrefix(a, url.QueryEscape("ACC-1/0002")) {
		t.Errorf("encodeFaves should be sorted, got %q", a)
	}
	// Round-trips through parseFaves.
	back := parseFaves(reqWithFaves(a))
	if !back["drv-050/0001"] || !back["ACC-1/0002"] {
		t.Errorf("encode→parse round-trip lost a ref: %v", back)
	}
}

// --- the sort (drv-050) ---

func TestApplyFavesPartitionsStable(t *testing.T) {
	// A column in alphabetical order; favouriting two non-adjacent cards must float
	// them to the top, alphabetical among themselves, non-favourites order untouched.
	col := func(ids ...string) []project.Attempt {
		var out []project.Attempt
		for _, id := range ids {
			out = append(out, project.Attempt{Ticket: id, ID: "0001"})
		}
		return out
	}
	vm := boardVM{Running: col("A", "B", "C", "D", "E")}
	faves := map[string]bool{"D/0001": true, "B/0001": true}
	applyFaves(&vm, faves)
	var order []string
	for _, a := range vm.Running {
		order = append(order, a.Ticket)
	}
	want := []string{"B", "D", "A", "C", "E"} // favourites first (alpha), then the rest (alpha)
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("favourites-first stable order wrong: got %v want %v", order, want)
	}
	// The Favourited bit is set on exactly the two favourited cards.
	for _, a := range vm.Running {
		fav := a.Ticket == "B" || a.Ticket == "D"
		if a.Favourited != fav {
			t.Errorf("card %s Favourited=%v, want %v", a.Ticket, a.Favourited, fav)
		}
	}
}

// --- the star control + toggle route (drv-050) ---

func TestStarButtonRendersOnEveryCard(t *testing.T) {
	h := newServerOver(t, seedColumns(t))
	board := get(t, h, "/board").Body.String()
	for _, id := range []string{"RUN-1", "STK-1", "REV-1", "DON-1"} {
		if !strings.Contains(board, `data-testid="star-btn-`+id+`-0001"`) {
			t.Errorf("card %s missing star button (rendered on every card):\n%s", id, board)
		}
	}
	// At rest (no cookie) every star reads un-favourited.
	if strings.Contains(board, `data-favourited="true"`) {
		t.Errorf("with no cookie no card should render favourited")
	}
	// The hover-reveal + on-state-stays-visible rules ship in the stylesheet.
	css := get(t, h, "/static/style.css").Body.String()
	for _, want := range []string{
		".star-form { margin: 0; opacity: 0;",
		".card-wrap:hover .star-form",
		".card-wrap:focus-within .star-form",
		".star-form.star-on { opacity: 1; }",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css missing star rule %q", want)
		}
	}
}

func TestStarToggleCookieAndOrder(t *testing.T) {
	root := seedColumns(t)
	h := newServerOver(t, root)

	// Favourite the Running card via the toggle route.
	rr := post(t, h, "/ticket/RUN-1/0001/star")
	if rr.Code != 200 {
		t.Fatalf("POST star: status %d", rr.Code)
	}
	// The response sets the favourites cookie containing the ref.
	setCookie := rr.Result().Cookies()
	var faveVal string
	for _, c := range setCookie {
		if c.Name == favesCookie {
			faveVal = c.Value
		}
	}
	if faveVal == "" {
		t.Fatalf("star POST must set the %s cookie, got %v", favesCookie, setCookie)
	}
	if !parseFaves(reqWithFaves(faveVal))["RUN-1/0001"] {
		t.Errorf("cookie should now contain RUN-1/0001, got %q", faveVal)
	}
	// The re-rendered board marks the card favourited.
	if !strings.Contains(rr.Body.String(), `data-testid="star-btn-RUN-1-0001" data-favourited="true"`) {
		t.Errorf("re-rendered card should be favourited:\n%s", rr.Body.String())
	}

	// The server persisted NOTHING: no durable event of any favourite kind on the log.
	events, err := ticketlog.Read(root, "RUN-1", "0001")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if strings.Contains(e.Type, "star") || strings.Contains(e.Type, "fav") {
			t.Errorf("favourite must write no durable event, found %q", e.Type)
		}
	}

	// Toggling again (resending the cookie) unfavourites: the cookie is cleared.
	r2 := httptest.NewRequest(http.MethodPost, "/ticket/RUN-1/0001/star", nil)
	r2.AddCookie(&http.Cookie{Name: favesCookie, Value: faveVal})
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, r2)
	if rr2.Code != 200 {
		t.Fatalf("second star POST: status %d", rr2.Code)
	}
	var cleared bool
	for _, c := range rr2.Result().Cookies() {
		if c.Name == favesCookie && (c.Value == "" || c.MaxAge < 0) {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("unfavouriting the last card should clear the cookie, got %v", rr2.Result().Cookies())
	}
	if strings.Contains(rr2.Body.String(), `data-testid="star-btn-RUN-1-0001" data-favourited="true"`) {
		t.Errorf("after unfavouriting, the card must not render favourited")
	}
}

func TestStarUnknownAttemptAndCrossOrigin(t *testing.T) {
	h := newServerOver(t, seedColumns(t))

	// Unknown attempt → 404, nothing set.
	rr := post(t, h, "/ticket/NOPE-9/0001/star")
	if rr.Code != http.StatusNotFound {
		t.Errorf("star for unknown attempt should 404, got %d", rr.Code)
	}

	// Cross-origin → refused, nothing set.
	r := httptest.NewRequest(http.MethodPost, "/ticket/RUN-1/0001/star", nil)
	r.Host = "board.local"
	r.Header.Set("Origin", "http://evil.example")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, r)
	if rr2.Code != http.StatusForbidden {
		t.Errorf("cross-origin star should be refused (403), got %d", rr2.Code)
	}
}

// TestBoardOrdersFavouritesFirstFromCookie proves the end-to-end read path: a board
// GET carrying a favourites cookie renders the favourited card at the top of its
// column, and the SAME cookie drives the SAME order on a second render (the 3s poll).
func TestBoardOrdersFavouritesFirstFromCookie(t *testing.T) {
	root := seedColumns(t)
	// Add a second Running card so there is an order to change.
	if err := root.EnsureAttemptDirs("RUN-2", "0001"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.SpecPath("RUN-2"), []byte("---\nid: RUN-2\ntitle: RUN-2\n---\n\nspec"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, "RUN-2", "0001", event.Event{Type: "created", Actor: "a", Body: "start"}); err != nil {
		t.Fatal(err)
	}
	h := newServerOver(t, root)

	// Plain (no cookie): alphabetical — RUN-1 before RUN-2.
	plain := get(t, h, "/board").Body.String()
	if idx(plain, "card-RUN-1-0001") > idx(plain, "card-RUN-2-0001") {
		t.Errorf("plain board should be alphabetical (RUN-1 before RUN-2)")
	}

	// Favourite RUN-2: it must now render before RUN-1, and identically on a re-render.
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodGet, "/board", nil)
		r.AddCookie(&http.Cookie{Name: favesCookie, Value: url.QueryEscape("RUN-2/0001")})
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		body := rr.Body.String()
		if idx(body, "card-RUN-2-0001") > idx(body, "card-RUN-1-0001") {
			t.Errorf("render %d: favourited RUN-2 should sort above RUN-1\n%s", i, body)
		}
	}
}

func idx(s, sub string) int { return strings.Index(s, sub) }
