package web

import (
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/pyramid"
)

func promoEv(seq int, env, commit, outcome string) event.Event {
	return event.Event{Seq: seq, Type: "promotion-result", Environment: env, Commit: commit, Outcome: outcome}
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
		if projectPromoChips(nil, nil, head) != nil {
			t.Error("nil pyramid should yield no chips")
		}
		if projectPromoChips(&pyramid.Pyramid{}, nil, head) != nil {
			t.Error("a pyramid with no declared promotion should yield no chips")
		}
	})

	t.Run("declared-but-never-run envs are grey none chips", func(t *testing.T) {
		chips := projectPromoChips(promoPyramid, nil, head)
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
		chips := projectPromoChips(promoPyramid, events, head)
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
		chips := projectPromoChips(promoPyramid, []event.Event{promoEv(1, "dev", head, "fail")}, head)
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
		chips := projectPromoChips(promoPyramid, events, head)
		if len(chips) != 2 || chips[0].Env != "dev" || chips[0].Class != "fail" {
			t.Fatalf("want dev/fail (+ staging none), ghost dropped, got %+v", chips)
		}
		if chips[1].Env != "staging" || chips[1].Class != "none" {
			t.Errorf("staging should be the grey none chip, got %+v", chips[1])
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
	}, head)
	if !allPromoGreen(allGreen) {
		t.Errorf("every promotion green-at-HEAD should be ratifiable, got %+v", allGreen)
	}
	oneStale := projectPromoChips(promoPyramid, []event.Event{
		promoEv(1, "dev", head, "pass"),
		promoEv(2, "staging", old, "pass"), // stale → withholds the tick
	}, head)
	if allPromoGreen(oneStale) {
		t.Error("a stale promotion must withhold the tick")
	}
	oneUnrun := projectPromoChips(promoPyramid, []event.Event{
		promoEv(1, "dev", head, "pass"), // staging never run → grey none
	}, head)
	if allPromoGreen(oneUnrun) {
		t.Error("an un-run promotion must withhold the tick")
	}
}

// projectPromoteSurface gates on Review and projects one panel per declared promotion,
// with per-env status lines derived from the latest result folded against HEAD.
func TestProjectPromoteSurface(t *testing.T) {
	const head = "cafef00d"
	const old = "deadbeef"

	t.Run("nil outside Review or without promotions", func(t *testing.T) {
		running := project.Attempt{State: project.Running}
		if projectPromoteSurface(running, promoPyramid, head) != nil {
			t.Error("a non-Review attempt should get no promote surface")
		}
		review := project.Attempt{State: project.Review}
		if projectPromoteSurface(review, &pyramid.Pyramid{}, head) != nil {
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
		vm := projectPromoteSurface(a, promoPyramid, head)
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
		vm := projectPromoteSurface(a, promoPyramid, head)
		if vm == nil || len(vm.Panels) != 2 {
			t.Fatalf("want 2 resting panels, got %+v", vm)
		}
		for _, p := range vm.Panels {
			if p.Class != "none" {
				t.Errorf("env %q should rest at class 'none', got %q", p.EnvName, p.Class)
			}
		}
	})
}
