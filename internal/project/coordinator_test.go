package project

import (
	"testing"

	"github.com/Dawil/draiver/internal/event"
)

// TestIsCoordinator pins the manager gate: a ticket is a coordinator iff it
// `wants:` something — the same len(edges.Wants)>0 the panel and the daemon use.
func TestIsCoordinator(t *testing.T) {
	edges := map[string]Edges{
		"CAP":   {Wants: []string{"C1"}},
		"PLAIN": {},
		"AFTER": {After: []string{"X"}}, // after:/requires: are not wants: — not a coordinator
	}
	if !IsCoordinator("CAP", edges) {
		t.Error("CAP wants C1 — want coordinator")
	}
	if IsCoordinator("PLAIN", edges) {
		t.Error("PLAIN wants nothing — not a coordinator")
	}
	if IsCoordinator("AFTER", edges) {
		t.Error("an after: edge is not a wants: edge — not a coordinator")
	}
	if IsCoordinator("ABSENT", edges) {
		t.Error("a ticket with no edge entry is not a coordinator")
	}
}

// TestDeriveCoordinatorSignal exercises the board's independent read of the wake
// state: the latest in-flight activation's child is the "why", and unhandled child
// doorbells are the queue depth. It mirrors the reconciler's derivation without
// importing it.
func TestDeriveCoordinatorSignal(t *testing.T) {
	edges := map[string]Edges{"CAP": {Wants: []string{"C1", "C2", "C3"}}}

	// C1: two doorbells (escalation #2, done #3). C2: one (review #2). C3: wanted but
	// no attempt yet. A non-trigger child event (a note) must not count as a doorbell.
	children := []Attempt{
		{Ticket: "C1", ID: "0001", Events: []event.Event{
			{Seq: 1, Type: "created"},
			{Seq: 2, Type: "escalation"},
			{Seq: 3, Type: "done"},
		}},
		{Ticket: "C2", ID: "0001", Events: []event.Event{
			{Seq: 1, Type: "note"}, // not a doorbell
			{Seq: 2, Type: "review"},
		}},
	}

	t.Run("woken, one handled doorbell, two queued behind", func(t *testing.T) {
		// The coordinator was woken for C1 #2 (the activation names it). C1 #3, C2 #2
		// remain unhandled ⇒ queue depth 2; the active wake's child is C1.
		coord := Attempt{Ticket: "CAP", ID: "0001", Events: []event.Event{
			{Seq: 1, Type: "created"},
			{Seq: 2, Type: WakeEventType, WokenChild: "C1", WokenSeq: 2},
		}}
		all := append([]Attempt{coord}, children...)
		sig := DeriveCoordinatorSignal(coord, all, edges)
		if !sig.Woken || sig.WakeChild != "C1" {
			t.Errorf("want woken re C1, got %+v", sig)
		}
		if sig.QueueDepth != 2 {
			t.Errorf("want queue depth 2 (C1#3, C2#2), got %d", sig.QueueDepth)
		}
	})

	t.Run("never woken: no why, full queue", func(t *testing.T) {
		coord := Attempt{Ticket: "CAP", ID: "0001", Events: []event.Event{
			{Seq: 1, Type: "created"},
		}}
		all := append([]Attempt{coord}, children...)
		sig := DeriveCoordinatorSignal(coord, all, edges)
		if sig.Woken || sig.WakeChild != "" {
			t.Errorf("never woken — want no why, got %+v", sig)
		}
		if sig.QueueDepth != 3 {
			t.Errorf("want queue depth 3 (all doorbells unhandled), got %d", sig.QueueDepth)
		}
	})

	t.Run("parked after wake: not surfaced as in-flight, queue still counts", func(t *testing.T) {
		// A park later than the activation ⇒ winding down, so Woken is false and the
		// why is suppressed — but the handled doorbell stays handled (queue excludes it).
		coord := Attempt{Ticket: "CAP", ID: "0001", Events: []event.Event{
			{Seq: 1, Type: "created"},
			{Seq: 2, Type: WakeEventType, WokenChild: "C1", WokenSeq: 2},
			{Seq: 3, Type: "park"},
		}}
		all := append([]Attempt{coord}, children...)
		sig := DeriveCoordinatorSignal(coord, all, edges)
		if sig.Woken || sig.WakeChild != "" {
			t.Errorf("parked — want no in-flight why, got %+v", sig)
		}
		if sig.QueueDepth != 2 {
			t.Errorf("want queue depth 2 (C1#2 still handled), got %d", sig.QueueDepth)
		}
	})

	t.Run("latest activation wins the why", func(t *testing.T) {
		coord := Attempt{Ticket: "CAP", ID: "0001", Events: []event.Event{
			{Seq: 1, Type: "created"},
			{Seq: 2, Type: WakeEventType, WokenChild: "C1", WokenSeq: 2},
			{Seq: 3, Type: WakeEventType, WokenChild: "C2", WokenSeq: 2},
		}}
		all := append([]Attempt{coord}, children...)
		sig := DeriveCoordinatorSignal(coord, all, edges)
		if sig.WakeChild != "C2" {
			t.Errorf("want latest activation child C2, got %q", sig.WakeChild)
		}
		// Both C1#2 and C2#2 are handled; only C1#3 remains ⇒ depth 1.
		if sig.QueueDepth != 1 {
			t.Errorf("want queue depth 1 (only C1#3), got %d", sig.QueueDepth)
		}
	})
}
