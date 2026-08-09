package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
)

// postProvenance posts the provenance form (repo/base) the way the panel does,
// with a same-origin Origin so the guard passes. A caller passes "" for a field
// it wants to leave out of the submit (a blank input the handler maps to "no
// change").
func postProvenance(t *testing.T, h http.Handler, id, att, repo, base string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"repo": {repo}, "base": {base}}
	req := httptest.NewRequest(http.MethodPost, "/ticket/"+id+"/"+att+"/provenance", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func provenanceOf(t *testing.T, root store.Root, id, att string) project.Attempt {
	t.Helper()
	a, err := project.LoadAttempt(root, id, att)
	if err != nil {
		t.Fatalf("LoadAttempt %s/%s: %v", id, att, err)
	}
	return a
}

// TestProvenancePanelAlwaysRendersPrefilled pins that the panel is on every
// attempt page, with the two inputs prefilled from the attempt's recorded
// repo/base so it reads as "edit provenance" — and that an attempt recording
// neither still shows the (empty) panel.
func TestProvenancePanelAlwaysRendersPrefilled(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// A Running attempt that records repo+base: the panel prefills both.
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/repo", "main")
	body := get(t, h, "/ticket/PROJ-3/0001").Body.String()
	for _, want := range []string{
		`data-testid="provenance"`,
		`data-testid="provenance-repo"`,
		`data-testid="provenance-base"`,
		`value="/tmp/repo"`,
		`value="main"`,
		`/ticket/PROJ-3/0001/provenance`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("provenance panel missing %q", want)
		}
	}
	// A Running attempt records no base, so no callout (that is a Review-only flag).
	if strings.Contains(body, `data-testid="provenance-callout"`) {
		t.Errorf("a Running attempt must not show the base-less callout")
	}

	// An attempt with no attempt.md still shows the panel, with empty inputs.
	empty := get(t, h, "/ticket/PROJ-1/0002").Body.String()
	if !strings.Contains(empty, `data-testid="provenance"`) {
		t.Errorf("the panel should render even when the attempt records no provenance")
	}
	if !strings.Contains(empty, `name="repo" class="provenance-input" data-testid="provenance-repo"`) {
		t.Errorf("expected an empty repo input on a provenance-less attempt")
	}
}

// TestProvenanceInlineEditTable pins the redesigned panel: a quiet key/value
// table above the spec, where each value is an inline input (no Save button) that
// posts on change, and each key carries an (i) info bubble whose tooltip explains
// the field.
func TestProvenanceInlineEditTable(t *testing.T) {
	root := seedBoard(t)
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/repo", "main")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	body := get(t, h, "/ticket/PROJ-3/0001").Body.String()

	// The table form posts on change (blur that altered a value) or Enter — there
	// is no Save button anymore.
	for _, want := range []string{
		`class="provenance-table"`,
		`hx-trigger="change, submit"`,
		`hx-swap="outerHTML"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("inline-edit table missing %q", want)
		}
	}
	if strings.Contains(body, `data-testid="provenance-submit"`) || strings.Contains(body, ">Save<") {
		t.Errorf("the redesigned panel must not carry a Save button")
	}

	// An (i) bubble per key, with a hover/focus tooltip (data-tip) mirrored to
	// aria-label for screen readers.
	if n := strings.Count(body, `class="provenance-info"`); n != 2 {
		t.Errorf("want an info bubble on each of the two keys, got %d", n)
	}
	if !strings.Contains(body, `data-tip=`) || !strings.Contains(body, `aria-label="base"`) {
		t.Errorf("info bubble tooltip / input aria-label missing")
	}

	// The provenance panel renders above the spec section.
	if pi, si := strings.Index(body, `data-testid="provenance"`), strings.Index(body, `data-testid="spec"`); pi < 0 || si < 0 || pi > si {
		t.Errorf("provenance panel should render above the spec (provenance@%d, spec@%d)", pi, si)
	}
}

// TestProvenanceCalloutOnBaselessReview pins the one prominent case: a Review
// attempt with no base (the state that blocks `ctl merge`/`sync`) gets a callout;
// a Review attempt that records a base does not.
func TestProvenanceCalloutOnBaselessReview(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// PROJ-2/0001 is Review and records no base → callout.
	baseless := get(t, h, "/ticket/PROJ-2/0001").Body.String()
	if !strings.Contains(baseless, `data-testid="provenance-callout"`) {
		t.Errorf("a base-less Review attempt should flag the merge-blocking state")
	}

	// Give it a base → the callout is gone.
	writeAttemptMeta(t, root, "PROJ-2", "0001", "/tmp/repo", "main")
	withBase := get(t, h, "/ticket/PROJ-2/0001").Body.String()
	if strings.Contains(withBase, `data-testid="provenance-callout"`) {
		t.Errorf("a Review attempt that records a base should not show the callout")
	}
}

// TestProvenanceSaveWritesViaCLI is the end-to-end write: the POST shells out to
// `draiver attempt set`, which persists repo+base into attempt.md, and the
// response re-renders the panel with the saved values and a confirmation.
func TestProvenanceSaveWritesViaCLI(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	rr := postProvenance(t, h, "PROJ-3", "0001", "/tmp/checkout", "release/v2")
	if rr.Code != 200 {
		t.Fatalf("POST provenance = %d, want 200\n%s", rr.Code, rr.Body.String())
	}
	if a := provenanceOf(t, root, "PROJ-3", "0001"); a.Repo != "/tmp/checkout" || a.Base != "release/v2" {
		t.Errorf("attempt.md not written via the verb: repo=%q base=%q", a.Repo, a.Base)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`data-testid="provenance"`,
		`value="/tmp/checkout"`,
		`value="release/v2"`,
		`data-testid="provenance-saved"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("save response missing %q\n%s", want, body)
		}
	}
}

// TestProvenanceSaveUnwedgesReview is the ticket's reason to exist: a base-less
// Review attempt gets its base from the UI, and the re-rendered panel drops the
// merge-blocking callout — the attempt is now landable.
func TestProvenanceSaveUnwedgesReview(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// Confirm it starts wedged (Review, no base → callout).
	if before := get(t, h, "/ticket/PROJ-2/0001").Body.String(); !strings.Contains(before, `data-testid="provenance-callout"`) {
		t.Fatalf("expected the base-less Review attempt to start with a callout")
	}

	rr := postProvenance(t, h, "PROJ-2", "0001", "", "main")
	if rr.Code != 200 {
		t.Fatalf("POST provenance = %d, want 200\n%s", rr.Code, rr.Body.String())
	}
	if a := provenanceOf(t, root, "PROJ-2", "0001"); a.Base != "main" {
		t.Errorf("base not set: %q", a.Base)
	}
	if body := rr.Body.String(); strings.Contains(body, `data-testid="provenance-callout"`) {
		t.Errorf("the callout should be gone once a base is recorded\n%s", body)
	}
}

// TestProvenanceBlankLeavesFieldUnchanged pins the "blank = no change" mapping: a
// blank input omits its flag from the shell-out, so `attempt set` leaves that
// field untouched rather than clearing it.
func TestProvenanceBlankLeavesFieldUnchanged(t *testing.T) {
	root := seedBoard(t)
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/orig", "orig-base")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// Change only the repo; leave base blank.
	if rr := postProvenance(t, h, "PROJ-3", "0001", "/tmp/new", ""); rr.Code != 200 {
		t.Fatalf("POST provenance = %d, want 200\n%s", rr.Code, rr.Body.String())
	}
	a := provenanceOf(t, root, "PROJ-3", "0001")
	if a.Repo != "/tmp/new" {
		t.Errorf("repo not updated: %q", a.Repo)
	}
	if a.Base != "orig-base" {
		t.Errorf("a blank base input must leave the base untouched, got %q", a.Base)
	}
}

// TestProvenanceAllBlankIsBadRequest pins that a submit that would change nothing
// is a 400 (not a 500 from the verb's "nothing to set"), and writes nothing.
func TestProvenanceAllBlankIsBadRequest(t *testing.T) {
	root := seedBoard(t)
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/orig", "orig-base")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	if rr := postProvenance(t, h, "PROJ-3", "0001", "   ", ""); rr.Code != http.StatusBadRequest {
		t.Fatalf("all-blank POST = %d, want 400\n%s", rr.Code, rr.Body.String())
	}
	if a := provenanceOf(t, root, "PROJ-3", "0001"); a.Repo != "/tmp/orig" || a.Base != "orig-base" {
		t.Errorf("an all-blank submit must write nothing: repo=%q base=%q", a.Repo, a.Base)
	}
}

// TestProvenanceGuards covers the state-changing route's guards: a cross-origin
// POST is refused (and writes nothing) and an unknown attempt 404s.
func TestProvenanceGuards(t *testing.T) {
	root := seedBoard(t)
	writeAttemptMeta(t, root, "PROJ-3", "0001", "/tmp/orig", "orig-base")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	rr := httptest.NewRecorder()
	form := url.Values{"repo": {"/tmp/evil"}, "base": {"evil"}}
	req := httptest.NewRequest(http.MethodPost, "/ticket/PROJ-3/0001/provenance", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d, want 403", rr.Code)
	}
	if a := provenanceOf(t, root, "PROJ-3", "0001"); a.Repo != "/tmp/orig" {
		t.Errorf("a blocked POST changed repo to %q", a.Repo)
	}

	if rr := postProvenance(t, h, "PROJ-3", "9999", "/tmp/x", "main"); rr.Code != http.StatusNotFound {
		t.Errorf("unknown attempt = %d, want 404", rr.Code)
	}
}
