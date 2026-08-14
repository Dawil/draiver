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

// postEdges posts the edge editor's whole-set form (all three relations) the way
// the panel does, with a same-origin Origin so the guard passes.
func postEdges(t *testing.T, h http.Handler, id, wants, after, requires string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"wants": {wants}, "after": {after}, "requires": {requires}}
	req := httptest.NewRequest(http.MethodPost, "/ticket/"+id+"/edges", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func edgesOf(t *testing.T, root store.Root, id string) project.Edges {
	t.Helper()
	e, err := project.LoadEdges(root, id)
	if err != nil {
		t.Fatalf("LoadEdges %s: %v", id, err)
	}
	return e
}

// TestEdgesPanelOnIndexNotDetail pins the placement acceptance: the editor is on
// the ticket-level attempts-index page, never the per-attempt detail page.
func TestEdgesPanelOnIndexNotDetail(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	index := get(t, h, "/ticket/PROJ-1").Body.String()
	for _, want := range []string{
		`data-testid="edges"`,
		`data-testid="edges-form"`,
		`data-testid="edges-wants"`,
		`data-testid="edges-after"`,
		`data-testid="edges-requires"`,
		`/ticket/PROJ-1/edges`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("index page missing edge editor %q", want)
		}
	}

	// The per-attempt detail page must NOT carry the edge editor — edges are
	// spec-level, edited once at the ticket level.
	detail := get(t, h, "/ticket/PROJ-1/0001").Body.String()
	if strings.Contains(detail, `data-testid="edges"`) {
		t.Error("the edge editor must not appear on the per-attempt detail page")
	}
}

// TestEdgesPrefillFromSpec pins that authored edges render back as the inputs'
// prefilled, comma-joined values on the index page.
func TestEdgesPrefillFromSpec(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	rr := postEdges(t, h, "PROJ-1", "PROJ-2, PROJ-3", "PROJ-2", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("save = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `data-testid="edges-saved"`) {
		t.Error("a successful save should confirm with saved ✓")
	}
	// The write landed in spec.md via the CLI.
	e := edgesOf(t, root, "PROJ-1")
	if strings.Join(e.Wants, ",") != "PROJ-2,PROJ-3" || strings.Join(e.After, ",") != "PROJ-2" {
		t.Fatalf("edges not written: %+v", e)
	}
	// A fresh GET prefills the inputs from the spec.
	body := get(t, h, "/ticket/PROJ-1").Body.String()
	if !strings.Contains(body, `value="PROJ-2, PROJ-3"`) {
		t.Errorf("wants input not prefilled:\n%s", body)
	}
}

// TestEdgesEditRemoves is the crux of the replace-semantics decision (#3): editing
// the field to drop an id must actually remove that edge, not silently union.
func TestEdgesEditRemoves(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	if rr := postEdges(t, h, "PROJ-1", "PROJ-2, PROJ-3", "", ""); rr.Code != http.StatusOK {
		t.Fatalf("first save = %d", rr.Code)
	}
	// Re-save with PROJ-2 removed from the field.
	if rr := postEdges(t, h, "PROJ-1", "PROJ-3", "", ""); rr.Code != http.StatusOK {
		t.Fatalf("second save = %d", rr.Code)
	}
	if e := edgesOf(t, root, "PROJ-1"); strings.Join(e.Wants, ",") != "PROJ-3" {
		t.Errorf("edit should remove PROJ-2, got wants=%v", e.Wants)
	}
	// Emptying the field clears the relation entirely.
	if rr := postEdges(t, h, "PROJ-1", "", "", ""); rr.Code != http.StatusOK {
		t.Fatalf("clear save = %d", rr.Code)
	}
	if e := edgesOf(t, root, "PROJ-1"); len(e.Wants) != 0 {
		t.Errorf("emptying the field should clear wants, got %v", e.Wants)
	}
}

// TestEdgesCycleRefusedInline pins the refusal acceptance: a cycle attempt shows
// the refusal inline (200 re-render, not a 5xx) and writes nothing to disk.
func TestEdgesCycleRefusedInline(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// PROJ-1 wants PROJ-2; then PROJ-2 after PROJ-1 would close a cycle.
	if rr := postEdges(t, h, "PROJ-1", "PROJ-2", "", ""); rr.Code != http.StatusOK {
		t.Fatalf("setup edge = %d", rr.Code)
	}
	before := edgesOf(t, root, "PROJ-2")

	rr := postEdges(t, h, "PROJ-2", "", "PROJ-1", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("cycle POST = %d, want an inline 200 re-render", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-testid="edges-error"`) {
		t.Errorf("cycle refusal should render inline:\n%s", body)
	}
	if !strings.Contains(strings.ToLower(body), "cycle") {
		t.Errorf("the inline error should name the cycle:\n%s", body)
	}
	// The re-render keeps the values the human tried, so the edit is not lost.
	if !strings.Contains(body, `value="PROJ-1"`) {
		t.Error("the refused values should survive in the re-rendered form")
	}
	// Nothing was written: PROJ-2's edges are unchanged.
	if after := edgesOf(t, root, "PROJ-2"); strings.Join(after.After, ",") != strings.Join(before.After, ",") {
		t.Errorf("a refused cycle must write nothing, edges changed to %+v", after)
	}
}

// TestEdgesGuards covers the write route's guards: an unknown ticket 404s and a
// cross-origin POST is blocked with 403 and writes nothing.
func TestEdgesGuards(t *testing.T) {
	root := seedBoard(t)
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	if rr := postEdges(t, h, "PROJ-404", "PROJ-1", "", ""); rr.Code != http.StatusNotFound {
		t.Errorf("edges POST to unknown ticket = %d, want 404", rr.Code)
	}

	form := url.Values{"wants": {"PROJ-2"}}
	req := httptest.NewRequest(http.MethodPost, "/ticket/PROJ-1/edges", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-origin edges POST = %d, want 403", rr.Code)
	}
	if e := edgesOf(t, root, "PROJ-1"); len(e.Wants) != 0 {
		t.Errorf("a blocked cross-origin POST must write nothing, got %v", e.Wants)
	}
}
