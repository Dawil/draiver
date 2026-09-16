package web

import (
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/pyramid"
)

// twoRung is the canonical unit→integration pyramid the tests project over.
func twoRung() *pyramid.Pyramid {
	return &pyramid.Pyramid{Levels: []pyramid.Level{{Name: "unit"}, {Name: "integration"}}}
}

func result(rung, commit string) event.Event {
	return event.Event{Type: "test-result", Rung: rung, Commit: commit}
}

// A nil pyramid (no .test-pyramid.yaml) always renders nothing, even with results.
func TestProjectPyramid_NoPyramid(t *testing.T) {
	if vm := projectPyramid(nil, []event.Event{result("unit", "abc")}, "abc"); vm != nil {
		t.Fatalf("nil pyramid must render nothing, got %+v", vm)
	}
}

// A declared pyramid with no logged test-result renders nothing (spec: no result → none).
func TestProjectPyramid_NoResult(t *testing.T) {
	events := []event.Event{{Type: "note", Body: "hi"}, {Type: "created"}}
	if vm := projectPyramid(twoRung(), events, "abc"); vm != nil {
		t.Fatalf("no test-result must render nothing, got %+v", vm)
	}
}

// Green-at-HEAD: the highest rung whose commit == HEAD, with every rung at or below
// it reached (climb-implied greens), and no stale flag.
func TestProjectPyramid_GreenAtHEAD(t *testing.T) {
	events := []event.Event{result("integration", "head1")}
	vm := projectPyramid(twoRung(), events, "head1")
	if vm == nil {
		t.Fatal("expected a green badge, got nil")
	}
	if vm.Stale {
		t.Errorf("green-at-HEAD must not be stale: %+v", vm)
	}
	if len(vm.Rungs) != 2 || !vm.Rungs[0].Reached || !vm.Rungs[1].Reached {
		t.Errorf("both rungs should be reached when integration is green at HEAD: %+v", vm.Rungs)
	}
}

// A lower rung green at HEAD leaves the rungs above it un-reached.
func TestProjectPyramid_GreenPartial(t *testing.T) {
	events := []event.Event{result("unit", "head1")}
	vm := projectPyramid(twoRung(), events, "head1")
	if vm == nil || vm.Stale {
		t.Fatalf("expected a non-stale green badge, got %+v", vm)
	}
	if !vm.Rungs[0].Reached || vm.Rungs[1].Reached {
		t.Errorf("only unit should be reached: %+v", vm.Rungs)
	}
}

// Stale: a result exists but none at HEAD — show the highest logged rung dimmed,
// with the SHA it was recorded at, not the live HEAD.
func TestProjectPyramid_Stale(t *testing.T) {
	events := []event.Event{result("integration", "old1")}
	vm := projectPyramid(twoRung(), events, "head2")
	if vm == nil {
		t.Fatal("expected a stale badge, got nil")
	}
	if !vm.Stale {
		t.Errorf("result not at HEAD must be stale: %+v", vm)
	}
	if vm.StaleAt != "old1" {
		t.Errorf("StaleAt should name the recorded commit, got %q", vm.StaleAt)
	}
	if vm.Head != "head2" {
		t.Errorf("Head should be the live HEAD, got %q", vm.Head)
	}
	if !vm.Rungs[1].Reached {
		t.Errorf("highest logged rung should show reached (dimmed): %+v", vm.Rungs)
	}
}

// A green result at HEAD wins even when an older stale result for a higher rung also
// exists: HEAD-truth beats history.
func TestProjectPyramid_GreenBeatsOlderHigher(t *testing.T) {
	events := []event.Event{
		result("integration", "old1"), // higher rung, but stale
		result("unit", "head3"),        // lower rung, at HEAD
	}
	vm := projectPyramid(twoRung(), events, "head3")
	if vm == nil || vm.Stale {
		t.Fatalf("a result at HEAD must win as green, got %+v", vm)
	}
	if !vm.Rungs[0].Reached || vm.Rungs[1].Reached {
		t.Errorf("green should track the at-HEAD rung (unit), not the stale higher one: %+v", vm.Rungs)
	}
}

// A result naming a rung absent from the current pyramid (renamed/removed) is
// ignored: with no other results, that projects to nothing.
func TestProjectPyramid_RenamedRung(t *testing.T) {
	events := []event.Event{result("e2e", "head1")}
	if vm := projectPyramid(twoRung(), events, "head1"); vm != nil {
		t.Fatalf("a result for an unknown rung must render nothing, got %+v", vm)
	}
}
