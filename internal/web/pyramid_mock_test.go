package web

import (
	"context"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/store"
)

// These are the integration tests drvweb-021 #32 asks for: mock the layer that
// fetches git state, then check the webui returns the correct badge values. They
// drive the real board + attempt-detail HTTP render, but swap s.pyramidGit — the
// injectable seam over the worktree/branch reads — for a deterministic stub, exactly
// as the dot tests swap s.alive. So they pin the whole render path (fold → VM →
// template → HTML) for every badge state without a git repo on the box, and prove
// the page reflects whatever the git layer reports: flip the stub's dirty/head and
// the same logged result renders green, dirty, or stale accordingly.
//
// This complements pyramid_render_test.go (real git, skips without it) and
// pyramid_test.go (the pure fold): here the git facts are mocked so the assertion is
// on the bytes the webui serves, deterministically, in CI.

// mockGit builds a pyramidGit stub that returns a fixed state for every attempt —
// the canned answer of a mocked git layer.
func mockGit(p *pyramid.Pyramid, head string, dirty bool, ok bool) func(context.Context, project.Attempt) (pyramidState, bool) {
	return func(context.Context, project.Attempt) (pyramidState, bool) {
		return pyramidState{Pyramid: p, Head: head, Dirty: dirty}, ok
	}
}

// mockServer seeds a Review attempt carrying the given events and returns a server
// whose git layer is the supplied stub — no real repo is touched.
func mockServer(t *testing.T, ticket, id string, git func(context.Context, project.Attempt) (pyramidState, bool), events ...event.Event) *Server {
	t.Helper()
	root := seedReviewAttempt(t, "/mock/repo", ticket, id, events...)
	s, err := New(store.Root{Dir: root.Dir})
	if err != nil {
		t.Fatal(err)
	}
	s.pyramidGit = git
	return s
}

// renderPages returns the board and attempt-detail HTML for an attempt, the two
// pages a reviewer sees the badge on.
func renderPages(t *testing.T, s *Server, ticket, id string) (board, detail string) {
	t.Helper()
	h := s.Handler()
	return get(t, h, "/").Body.String(), get(t, h, "/ticket/"+ticket+"/"+id).Body.String()
}

// greenLog is the canonical log: an attempt that logged integration green at `head`.
func greenLog(head string) []event.Event {
	return []event.Event{
		{Type: "created", Actor: "a"},
		{Type: "test-result", Actor: "agent:x", Rung: "integration", Commit: head},
		{Type: "review", Actor: "agent:x", Body: "please review"},
	}
}

// Mocked git reporting a clean tree at the logged commit → green badge on both pages.
func TestMockPyramid_GreenAtHEAD(t *testing.T) {
	ticket, id := "MPY-1", "0001"
	s := mockServer(t, ticket, id, mockGit(twoRung(), "head1", false, true), greenLog("head1")...)

	badge := `data-testid="pyramid-` + ticket + "-" + id + `"`
	board, detail := renderPages(t, s, ticket, id)
	for _, page := range []struct{ name, body string }{{"board", board}, {"detail", detail}} {
		if !strings.Contains(page.body, badge) {
			t.Fatalf("%s: badge %s not rendered", page.name, badge)
		}
		if !strings.Contains(page.body, "pyramid-green") {
			t.Errorf("%s: expected a green badge", page.name)
		}
		if strings.Contains(page.body, `data-stale="true"`) || strings.Contains(page.body, `data-dirty="true"`) {
			t.Errorf("%s: green must be neither stale nor dirty", page.name)
		}
		if !strings.Contains(page.body, "rung-check") {
			t.Errorf("%s: expected a reached-rung ✓", page.name)
		}
	}
}

// Same log, but the mocked git layer reports the tree dirty → the webui must render
// the dirty state, not green (drvweb-021 #27). Proves the page tracks the git fact:
// only the stub's dirty flag differs from the green case.
func TestMockPyramid_DirtyNotGreen(t *testing.T) {
	ticket, id := "MPY-2", "0001"
	s := mockServer(t, ticket, id, mockGit(twoRung(), "head1", true, true), greenLog("head1")...)

	badge := `data-testid="pyramid-` + ticket + "-" + id + `"`
	board, detail := renderPages(t, s, ticket, id)
	for _, page := range []struct{ name, body string }{{"board", board}, {"detail", detail}} {
		if !strings.Contains(page.body, badge) {
			t.Fatalf("%s: badge %s not rendered", page.name, badge)
		}
		if strings.Contains(page.body, "pyramid-green") {
			t.Errorf("%s: a dirty tree must not render green", page.name)
		}
		if !strings.Contains(page.body, "pyramid-dirty") || !strings.Contains(page.body, `data-dirty="true"`) {
			t.Errorf("%s: expected the dirty state (pyramid-dirty, data-dirty=true)", page.name)
		}
	}
}

// Same log, but the mocked HEAD has moved past the logged commit → stale, not green.
func TestMockPyramid_StaleWhenHeadMoved(t *testing.T) {
	ticket, id := "MPY-3", "0001"
	s := mockServer(t, ticket, id, mockGit(twoRung(), "head2", false, true), greenLog("old1")...)

	badge := `data-testid="pyramid-` + ticket + "-" + id + `"`
	board, detail := renderPages(t, s, ticket, id)
	for _, page := range []struct{ name, body string }{{"board", board}, {"detail", detail}} {
		if !strings.Contains(page.body, badge) {
			t.Fatalf("%s: badge %s not rendered", page.name, badge)
		}
		if strings.Contains(page.body, "pyramid-green") {
			t.Errorf("%s: a moved HEAD must not render green", page.name)
		}
		if !strings.Contains(page.body, "pyramid-stale") || !strings.Contains(page.body, `data-stale="true"`) {
			t.Errorf("%s: expected the stale state (pyramid-stale, data-stale=true)", page.name)
		}
	}
}

// Only unit was logged green at HEAD → the badge shows unit reached, integration not.
func TestMockPyramid_PartialGreen(t *testing.T) {
	ticket, id := "MPY-4", "0001"
	events := []event.Event{
		{Type: "created", Actor: "a"},
		{Type: "test-result", Actor: "agent:x", Rung: "unit", Commit: "head1"},
	}
	s := mockServer(t, ticket, id, mockGit(twoRung(), "head1", false, true), events...)

	badge := `data-testid="pyramid-` + ticket + "-" + id + `"`
	board, _ := renderPages(t, s, ticket, id)
	if !strings.Contains(board, badge) {
		t.Fatalf("board: badge %s not rendered", badge)
	}
	if !strings.Contains(board, "pyramid-green") {
		t.Errorf("board: partial-at-HEAD is still green through the reached rung")
	}
	// unit reached, integration not: exactly one rung carries the ✓ check.
	if n := strings.Count(board, "rung-check"); n != 1 {
		t.Errorf("board: expected exactly one reached rung (unit), got %d rung-check marks", n)
	}
}

// The git layer reports nothing to project (ok=false — no repo, no checkout, no
// branch, or a git error) → no badge, even with a test-result logged.
func TestMockPyramid_NoGitStateRendersNothing(t *testing.T) {
	ticket, id := "MPY-5", "0001"
	s := mockServer(t, ticket, id, mockGit(nil, "", false, false), greenLog("head1")...)

	badge := `data-testid="pyramid-` + ticket + "-" + id + `"`
	board, detail := renderPages(t, s, ticket, id)
	if strings.Contains(board, badge) {
		t.Errorf("board: badge rendered when the git layer reported no state")
	}
	if strings.Contains(detail, badge) {
		t.Errorf("detail: badge rendered when the git layer reported no state")
	}
}

// The git layer succeeds but the branch carries no .test-pyramid.yaml (nil pyramid)
// → no badge, even with a test-result logged.
func TestMockPyramid_NoPyramidRendersNothing(t *testing.T) {
	ticket, id := "MPY-6", "0001"
	s := mockServer(t, ticket, id, mockGit(nil, "head1", false, true), greenLog("head1")...)

	badge := `data-testid="pyramid-` + ticket + "-" + id + `"`
	board, detail := renderPages(t, s, ticket, id)
	if strings.Contains(board, badge) {
		t.Errorf("board: badge rendered for an attempt with no .test-pyramid.yaml")
	}
	if strings.Contains(detail, badge) {
		t.Errorf("detail: badge rendered for an attempt with no .test-pyramid.yaml")
	}
}

// A declared pyramid with no test-result logged renders nothing — the git layer has
// state, but there is no recorded verification to surface.
func TestMockPyramid_NoResultRendersNothing(t *testing.T) {
	ticket, id := "MPY-7", "0001"
	events := []event.Event{
		{Type: "created", Actor: "a"},
		{Type: "note", Actor: "a", Body: "no result yet"},
	}
	s := mockServer(t, ticket, id, mockGit(twoRung(), "head1", false, true), events...)

	badge := `data-testid="pyramid-` + ticket + "-" + id + `"`
	board, _ := renderPages(t, s, ticket, id)
	if strings.Contains(board, badge) {
		t.Errorf("board: badge rendered with no test-result event")
	}
}
