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
	if vm := projectPyramid(nil, []event.Event{result("unit", "abc")}, "abc", false); vm != nil {
		t.Fatalf("nil pyramid must render nothing, got %+v", vm)
	}
}

// A declared pyramid with no logged test-result renders nothing (spec: no result → none).
func TestProjectPyramid_NoResult(t *testing.T) {
	events := []event.Event{{Type: "note", Body: "hi"}, {Type: "created"}}
	if vm := projectPyramid(twoRung(), events, "abc", false); vm != nil {
		t.Fatalf("no test-result must render nothing, got %+v", vm)
	}
}

// Green-at-HEAD: the highest rung whose commit == HEAD, with every rung at or below
// it reached (climb-implied greens), and no stale flag.
func TestProjectPyramid_GreenAtHEAD(t *testing.T) {
	events := []event.Event{result("integration", "head1")}
	vm := projectPyramid(twoRung(), events, "head1", false)
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
	vm := projectPyramid(twoRung(), events, "head1", false)
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
	vm := projectPyramid(twoRung(), events, "head2", false)
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
		result("unit", "head3"),       // lower rung, at HEAD
	}
	vm := projectPyramid(twoRung(), events, "head3", false)
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
	if vm := projectPyramid(twoRung(), events, "head1", false); vm != nil {
		t.Fatalf("a result for an unknown rung must render nothing, got %+v", vm)
	}
}

// Dirty: a result names HEAD, but the worktree has uncommitted changes — the badge
// must not report green (drvweb-021 #27). It shows the reached rungs dimmed, flagged
// dirty (not stale, since HEAD has not moved).
func TestProjectPyramid_DirtySuppressesGreen(t *testing.T) {
	events := []event.Event{result("integration", "head1")}
	vm := projectPyramid(twoRung(), events, "head1", true)
	if vm == nil {
		t.Fatal("expected a dirty badge, got nil")
	}
	if !vm.Dirty {
		t.Errorf("uncommitted changes at HEAD must mark the badge dirty: %+v", vm)
	}
	if vm.Stale {
		t.Errorf("dirty is not stale — HEAD has not moved: %+v", vm)
	}
	if !vm.Rungs[0].Reached || !vm.Rungs[1].Reached {
		t.Errorf("the reached rungs should still show (dimmed): %+v", vm.Rungs)
	}
}

// A clean tree with the same at-HEAD result stays green — the dirty flag is the only
// difference, confirming cleanliness is what gates green.
func TestProjectPyramid_CleanStaysGreen(t *testing.T) {
	events := []event.Event{result("integration", "head1")}
	vm := projectPyramid(twoRung(), events, "head1", false)
	if vm == nil || vm.Dirty || vm.Stale {
		t.Fatalf("a clean tree at HEAD must be plain green, got %+v", vm)
	}
}

// A rung's targeted environment is carried onto its VM (drv-012), empty for the
// ambient context, so the badge can surface *where* green was proven.
func TestProjectPyramid_SurfacesEnvironment(t *testing.T) {
	p := &pyramid.Pyramid{
		Environments: []pyramid.Environment{{Name: "staging"}},
		Levels: []pyramid.Level{
			{Name: "unit"}, // ambient
			{Name: "integration", Environment: "staging"}, // bound
		},
	}
	vm := projectPyramid(p, []event.Event{result("integration", "h")}, "h", false)
	if vm == nil || len(vm.Rungs) != 2 {
		t.Fatalf("expected a 2-rung badge, got %+v", vm)
	}
	if vm.Rungs[0].Environment != "" {
		t.Errorf("ambient rung should carry no environment, got %q", vm.Rungs[0].Environment)
	}
	if vm.Rungs[1].Environment != "staging" {
		t.Errorf("bound rung should carry its environment, got %q", vm.Rungs[1].Environment)
	}
}

// Stale beats dirty: when the result is not at HEAD the badge is stale regardless of
// the working tree — a moved tip is the dominant fact, and both render non-green so
// "never green when dirty" still holds.
func TestProjectPyramid_StaleBeatsDirty(t *testing.T) {
	events := []event.Event{result("integration", "old1")}
	vm := projectPyramid(twoRung(), events, "head2", true)
	if vm == nil || !vm.Stale {
		t.Fatalf("a result behind HEAD must be stale, got %+v", vm)
	}
	if vm.Dirty {
		t.Errorf("stale takes precedence; Dirty should be unset: %+v", vm)
	}
}
