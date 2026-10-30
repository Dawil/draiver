package project

import (
	"testing"

	"github.com/Dawil/draiver/internal/event"
)

// promo builds a promotion-result event for the fold tests.
func promo(seq int, env, commit, outcome string) event.Event {
	return event.Event{Seq: seq, Type: "promotion-result", Environment: env, Commit: commit, Outcome: outcome}
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
}
