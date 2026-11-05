package web

import (
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/pyramid"
)

// testNow is the fixed render clock the pure chip/surface projections fold against.
// drv-049 threads a `now` into them so an abandoned in-progress marker can be
// reconciled past promoCheckStaleAfter; the pre-drv-049 cases below carry no
// in-progress marker, so the exact value is immaterial to them.
var testNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func promoEv(seq int, env, commit, outcome string) event.Event {
	return event.Event{Seq: seq, Type: "promotion-result", Environment: env, Commit: commit, Outcome: outcome}
}

// promoStarted / promoCleared build the drv-049 durable in-progress markers: a
// `started` marker (stamped at ts) opens the spinner state; a `cleared` marker
// closes it with no verdict (the blocked/env-fault path).
func promoStarted(seq int, env, commit string, ts time.Time) event.Event {
	return event.Event{Seq: seq, Type: "promotion-check", Environment: env, Commit: commit, Outcome: "started", TS: ts}
}

func promoCleared(seq int, env string) event.Event {
	return event.Event{Seq: seq, Type: "promotion-check", Environment: env, Outcome: "cleared"}
}

var promoPyramid = &pyramid.Pyramid{
	Environments: []pyramid.Environment{{Name: "dev"}, {Name: "staging"}},
	Promotions: []pyramid.Promotion{
		{Environment: "dev", QA: "true"},
		{Environment: "staging", QA: "true"},
	},
}

// projectPromoChips emits a chip for every DECLARED promotion (drv-046): a declared-
// but-never-run env is grey `none` (○), a pass at HEAD is ok (green ✓), a pass at an
// older commit is stale (muted ·), a fail is fail (✕). Chips render in declaration
// order, and a result for an undeclared promotion is dropped (the pyramid is the
// authority). Only a nil/empty-promotion pyramid yields no chips at all.
func TestProjectPromoChips(t *testing.T) {
	const head = "cafef00d"
	const old = "deadbeef"

	t.Run("nil only when no pyramid or no declared promotion", func(t *testing.T) {
		if projectPromoChips(nil, nil, head, testNow) != nil {
			t.Error("nil pyramid should yield no chips")
		}
		if projectPromoChips(&pyramid.Pyramid{}, nil, head, testNow) != nil {
			t.Error("a pyramid with no declared promotion should yield no chips")
		}
	})

	t.Run("declared-but-never-run envs are grey none chips", func(t *testing.T) {
		chips := projectPromoChips(promoPyramid, nil, head, testNow)
		if len(chips) != 2 {
			t.Fatalf("want 2 grey chips (one per declared promotion), got %d (%v)", len(chips), chips)
		}
		for _, c := range chips {
			if c.Class != "none" || c.Symbol != "○" {
				t.Errorf("an un-run promotion should be none/○, got %+v", c)
			}
		}
	})

	t.Run("none / ok / stale / fail classification in declaration order", func(t *testing.T) {
		events := []event.Event{
			promoEv(1, "staging", old, "pass"), // stale (older commit)
			promoEv(2, "dev", head, "pass"),    // ok (at HEAD)
		}
		chips := projectPromoChips(promoPyramid, events, head, testNow)
		if len(chips) != 2 {
			t.Fatalf("want 2 chips, got %d (%v)", len(chips), chips)
		}
		// Declaration order: dev first, staging second — not log order.
		if chips[0].Env != "dev" || chips[0].Class != "ok" || chips[0].Symbol != "✓" {
			t.Errorf("chip[0] should be dev/ok/✓, got %+v", chips[0])
		}
		if chips[1].Env != "staging" || chips[1].Class != "stale" || chips[1].Symbol != "·" {
			t.Errorf("chip[1] should be staging/stale/·, got %+v", chips[1])
		}
	})

	t.Run("a fail is a cross regardless of commit; un-run peer stays grey", func(t *testing.T) {
		chips := projectPromoChips(promoPyramid, []event.Event{promoEv(1, "dev", head, "fail")}, head, testNow)
		if len(chips) != 2 {
			t.Fatalf("want 2 chips (dev fail + staging none), got %+v", chips)
		}
		if chips[0].Env != "dev" || chips[0].Class != "fail" || chips[0].Symbol != "✕" {
			t.Errorf("dev should be fail/✕, got %+v", chips[0])
		}
		if chips[1].Env != "staging" || chips[1].Class != "none" {
			t.Errorf("staging should still be the grey none chip, got %+v", chips[1])
		}
	})

	t.Run("latest result wins and undeclared envs drop", func(t *testing.T) {
		events := []event.Event{
			promoEv(1, "dev", old, "pass"),    // superseded
			promoEv(2, "dev", head, "fail"),   // latest for dev
			promoEv(3, "ghost", head, "pass"), // not declared — dropped
		}
		chips := projectPromoChips(promoPyramid, events, head, testNow)
		if len(chips) != 2 || chips[0].Env != "dev" || chips[0].Class != "fail" {
			t.Fatalf("want dev/fail (+ staging none), ghost dropped, got %+v", chips)
		}
		if chips[1].Env != "staging" || chips[1].Class != "none" {
			t.Errorf("staging should be the grey none chip, got %+v", chips[1])
		}
	})
}

// TestProjectPromoChipsInProgress covers drv-049: a durable `promotion-check` started
// marker renders the chip as the spinning `run` state from server markup (so the 3s
// morph poll keeps redrawing it); a terminal verdict or a `cleared` marker settles it;
// and an abandoned marker older than promoCheckStaleAfter reconciles to its prior
// resting verdict rather than spinning forever.
func TestProjectPromoChipsInProgress(t *testing.T) {
	const head = "cafef00d"
	const old = "deadbeef"
	now := testNow
	fresh := now.Add(-5 * time.Second) // well within the staleness window
	stale := now.Add(-promoCheckStaleAfter - time.Minute)

	t.Run("a live started marker spins the chip (run), no glyph", func(t *testing.T) {
		chips := projectPromoChips(promoPyramid, []event.Event{
			promoStarted(1, "dev", head, fresh),
		}, head, now)
		if chips[0].Env != "dev" || chips[0].Class != "run" || chips[0].Symbol != "" {
			t.Errorf("an in-progress dev check should be run/no-glyph, got %+v", chips[0])
		}
		// Its declared peer with no marker stays the grey none chip.
		if chips[1].Class != "none" {
			t.Errorf("staging should still be none, got %+v", chips[1])
		}
	})

	t.Run("a terminal verdict after a start clears the spinner", func(t *testing.T) {
		chips := projectPromoChips(promoPyramid, []event.Event{
			promoStarted(1, "dev", head, fresh),
			promoEv(2, "dev", head, "pass"),
		}, head, now)
		if chips[0].Class != "ok" {
			t.Errorf("a pass@HEAD after the start should settle green, got %+v", chips[0])
		}
	})

	t.Run("a cleared marker (blocked/env-fault) settles back to the prior verdict", func(t *testing.T) {
		chips := projectPromoChips(promoPyramid, []event.Event{
			promoEv(1, "dev", head, "pass"),     // prior green
			promoStarted(2, "dev", head, fresh), // re-check starts
			promoCleared(3, "dev"),              // ended with no verdict
		}, head, now)
		if chips[0].Class != "ok" {
			t.Errorf("a cleared re-check must leave the prior pass@HEAD green, got %+v", chips[0])
		}
	})

	t.Run("an abandoned (stale) marker does not spin forever; falls back to prior verdict", func(t *testing.T) {
		// Prior pass at an OLDER commit, then a start that never terminated.
		chips := projectPromoChips(promoPyramid, []event.Event{
			promoEv(1, "dev", old, "pass"),
			promoStarted(2, "dev", head, stale),
		}, head, now)
		if chips[0].Class == "run" {
			t.Errorf("a marker older than the TTL must not keep spinning, got %+v", chips[0])
		}
		if chips[0].Class != "stale" {
			t.Errorf("it should fall back to the prior pass@old (stale), got %+v", chips[0])
		}
	})

	t.Run("an abandoned first-ever check falls back to grey none, not a false green", func(t *testing.T) {
		chips := projectPromoChips(promoPyramid, []event.Event{
			promoStarted(1, "dev", head, stale),
		}, head, now)
		if chips[0].Class != "none" {
			t.Errorf("an abandoned never-verdicted check must reconcile to none, got %+v", chips[0])
		}
	})
}

// allPromoGreen gates the mark-done tick: at least one declared promotion and every chip
// green-at-HEAD. An empty set (no declared promotion) is never ratifiable, and any grey/
// stale/fail chip withholds the tick.
func TestAllPromoGreen(t *testing.T) {
	const head = "cafef00d"
	const old = "deadbeef"

	if allPromoGreen(nil) {
		t.Error("no declared promotion must not be ratifiable")
	}
	allGreen := projectPromoChips(promoPyramid, []event.Event{
		promoEv(1, "dev", head, "pass"),
		promoEv(2, "staging", head, "pass"),
	}, head, testNow)
	if !allPromoGreen(allGreen) {
		t.Errorf("every promotion green-at-HEAD should be ratifiable, got %+v", allGreen)
	}
	oneStale := projectPromoChips(promoPyramid, []event.Event{
		promoEv(1, "dev", head, "pass"),
		promoEv(2, "staging", old, "pass"), // stale → withholds the tick
	}, head, testNow)
	if allPromoGreen(oneStale) {
		t.Error("a stale promotion must withhold the tick")
	}
	oneUnrun := projectPromoChips(promoPyramid, []event.Event{
		promoEv(1, "dev", head, "pass"), // staging never run → grey none
	}, head, testNow)
	if allPromoGreen(oneUnrun) {
		t.Error("an un-run promotion must withhold the tick")
	}
	// drv-049: a check in progress on an otherwise-green env is a `run` chip, so the
	// tick must be withheld mid-check (never ratify while a verdict is still in flight).
	oneRunning := projectPromoChips(promoPyramid, []event.Event{
		promoEv(1, "dev", head, "pass"),
		promoEv(2, "staging", head, "pass"),
		promoStarted(3, "dev", head, testNow.Add(-time.Second)), // dev re-check in flight
	}, head, testNow)
	if allPromoGreen(oneRunning) {
		t.Error("an in-progress check must withhold the mark-done tick")
	}
}

// projectPromoteSurface gates on Review and projects one panel per declared promotion,
// with per-env status lines derived from the latest result folded against HEAD.
func TestProjectPromoteSurface(t *testing.T) {
	const head = "cafef00d"
	const old = "deadbeef"

	t.Run("nil outside Review or without promotions", func(t *testing.T) {
		running := project.Attempt{State: project.Running}
		if projectPromoteSurface(running, promoPyramid, head, testNow) != nil {
			t.Error("a non-Review attempt should get no promote surface")
		}
		review := project.Attempt{State: project.Review}
		if projectPromoteSurface(review, &pyramid.Pyramid{}, head, testNow) != nil {
			t.Error("a pyramid with no promotions should get no surface")
		}
	})

	t.Run("one panel per promotion with derived status lines", func(t *testing.T) {
		a := project.Attempt{
			State: project.Review,
			Events: []event.Event{
				promoEv(1, "dev", head, "pass"),    // ok
				promoEv(2, "staging", old, "pass"), // stale
			},
		}
		vm := projectPromoteSurface(a, promoPyramid, head, testNow)
		if vm == nil || len(vm.Panels) != 2 {
			t.Fatalf("want 2 panels, got %+v", vm)
		}
		if vm.Panels[0].Class != "ok" {
			t.Errorf("dev panel should be ok, got %q (%q)", vm.Panels[0].Class, vm.Panels[0].StatusLine)
		}
		if vm.Panels[1].Class != "stale" {
			t.Errorf("staging panel should be stale, got %q (%q)", vm.Panels[1].Class, vm.Panels[1].StatusLine)
		}
	})

	t.Run("a promotion with no result is the resting not-yet-promoted state", func(t *testing.T) {
		a := project.Attempt{State: project.Review}
		vm := projectPromoteSurface(a, promoPyramid, head, testNow)
		if vm == nil || len(vm.Panels) != 2 {
			t.Fatalf("want 2 resting panels, got %+v", vm)
		}
		for _, p := range vm.Panels {
			if p.Class != "none" {
				t.Errorf("env %q should rest at class 'none', got %q", p.EnvName, p.Class)
			}
		}
	})

	t.Run("a live in-progress marker renders the panel run state (drv-049)", func(t *testing.T) {
		a := project.Attempt{
			State:  project.Review,
			Events: []event.Event{promoStarted(1, "dev", head, testNow.Add(-2*time.Second))},
		}
		vm := projectPromoteSurface(a, promoPyramid, head, testNow)
		if vm == nil || vm.Panels[0].Class != "run" {
			t.Errorf("dev panel should be run while in progress, got %+v", vm.Panels[0])
		}
	})

	t.Run("an abandoned in-progress marker does not pin the panel spinning", func(t *testing.T) {
		a := project.Attempt{
			State:  project.Review,
			Events: []event.Event{promoStarted(1, "dev", head, testNow.Add(-promoCheckStaleAfter-time.Minute))},
		}
		vm := projectPromoteSurface(a, promoPyramid, head, testNow)
		if vm.Panels[0].Class == "run" {
			t.Errorf("a stale panel marker must not keep spinning, got %+v", vm.Panels[0])
		}
	})
}
