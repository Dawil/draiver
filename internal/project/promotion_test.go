package project

import (
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/event"
)

// promo builds a promotion-result event for the fold tests.
func promo(seq int, env, commit, outcome string) event.Event {
	return event.Event{Seq: seq, Type: "promotion-result", Environment: env, Commit: commit, Outcome: outcome}
}

// checkEv builds a drv-049 `promotion-check` marker (Outcome "started"/"cleared").
func checkEv(seq int, env, commit, outcome string, ts time.Time) event.Event {
	return event.Event{Seq: seq, Type: "promotion-check", Environment: env, Commit: commit, Outcome: outcome, TS: ts}
}

// DerivePromotions folds promotion-result events into the latest status per
// environment: last-event-wins, keyed by environment, ignoring non-promotion events
// and events with a blank environment.
func TestDerivePromotions(t *testing.T) {
	t.Run("no promotion events yields an empty map", func(t *testing.T) {
		got := DerivePromotions([]event.Event{{Seq: 1, Type: "created"}, {Seq: 2, Type: "decision"}})
		if len(got) != 0 {
			t.Fatalf("want empty, got %v", got)
		}
	})

	t.Run("latest result per environment wins", func(t *testing.T) {
		got := DerivePromotions([]event.Event{
			{Seq: 1, Type: "created"},
			promo(2, "dev", "aaa", "pass"),
			promo(3, "staging", "aaa", "pass"),
			promo(4, "dev", "bbb", "fail"), // supersedes the earlier dev pass
		})
		if len(got) != 2 {
			t.Fatalf("want 2 envs, got %d (%v)", len(got), got)
		}
		if got["dev"].Commit != "bbb" || got["dev"].Outcome != "fail" {
			t.Errorf("dev should fold to the latest (bbb/fail), got %+v", got["dev"])
		}
		if got["staging"].Commit != "aaa" || got["staging"].Outcome != "pass" {
			t.Errorf("staging should be aaa/pass, got %+v", got["staging"])
		}
	})

	t.Run("a blank environment is ignored", func(t *testing.T) {
		got := DerivePromotions([]event.Event{promo(1, "  ", "aaa", "pass")})
		if len(got) != 0 {
			t.Fatalf("a blank-environment result names nothing to key on, want empty, got %v", got)
		}
	})

	t.Run("non-promotion events are ignored", func(t *testing.T) {
		got := DerivePromotions([]event.Event{
			{Seq: 1, Type: "test-result", Environment: "dev", Commit: "aaa", Outcome: "pass"},
		})
		if len(got) != 0 {
			t.Fatalf("a test-result is not a promotion-result, want empty, got %v", got)
		}
	})

	// drv-049: a `promotion-check` started marker opens the durable in-progress state;
	// a terminal `promotion-result` or a `cleared` marker closes it.
	ts := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)

	t.Run("a started marker marks the env in progress and records its timestamp", func(t *testing.T) {
		got := DerivePromotions([]event.Event{checkEv(1, "dev", "aaa", "started", ts)})
		s := got["dev"]
		if !s.InProgress || !s.StartedAt.Equal(ts) {
			t.Fatalf("dev should be in progress at %v, got %+v", ts, s)
		}
		// A started marker carries no verdict: Outcome/Commit stay empty so a later TTL
		// reconcile falls back to none, never a false green.
		if s.Outcome != "" || s.Commit != "" {
			t.Errorf("a started marker must not set a verdict, got %+v", s)
		}
	})

	t.Run("a promotion-result after a start clears in progress", func(t *testing.T) {
		got := DerivePromotions([]event.Event{
			checkEv(1, "dev", "aaa", "started", ts),
			promo(2, "dev", "aaa", "pass"),
		})
		s := got["dev"]
		if s.InProgress || s.Outcome != "pass" || s.Commit != "aaa" {
			t.Errorf("a terminal verdict should clear in progress and record pass@aaa, got %+v", s)
		}
	})

	t.Run("a cleared marker closes in progress without disturbing the prior verdict", func(t *testing.T) {
		got := DerivePromotions([]event.Event{
			promo(1, "dev", "aaa", "pass"),
			checkEv(2, "dev", "bbb", "started", ts),
			checkEv(3, "dev", "", "cleared", ts),
		})
		s := got["dev"]
		if s.InProgress {
			t.Errorf("a cleared marker should close in progress, got %+v", s)
		}
		if s.Outcome != "pass" || s.Commit != "aaa" {
			t.Errorf("the prior pass@aaa must survive a cleared re-check, got %+v", s)
		}
	})
}
