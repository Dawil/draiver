package project

import (
	"testing"

	"github.com/Dawil/draiver/internal/event"
)

// TestDeriveInterrupted pins the interrupt hold's projection (drvctl-060): the
// latest `interrupt` holds until a human act — a message to the agent, a
// resolution, an enable or a `ctl start` — and daemon bookkeeping does not lift it.
func TestDeriveInterrupted(t *testing.T) {
	intr := event.Event{Seq: 3, Type: InterruptType}
	cases := []struct {
		name   string
		events []event.Event
		held   bool
	}{
		{"no interrupt", []event.Event{{Seq: 1, Type: "note"}}, false},
		{"bare interrupt holds", []event.Event{{Seq: 1, Type: "created"}, intr}, true},
		{"daemon note keeps the hold", []event.Event{intr, {Seq: 4, Type: "note"}, {Seq: 5, Type: "test-result"}}, true},
		{"message to agent lifts", []event.Event{intr, {Seq: 4, Type: "note", To: "agent"}}, false},
		{"resolution lifts", []event.Event{intr, {Seq: 4, Type: "resolution"}}, false},
		{"enable lifts", []event.Event{intr, {Seq: 4, Type: "enable"}}, false},
		{"ctl start lifts", []event.Event{intr, {Seq: 4, Type: StartType}}, false},
		{"re-interrupt after lift holds again", []event.Event{intr, {Seq: 4, Type: StartType}, {Seq: 5, Type: InterruptType}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			held, ev := DeriveInterrupted(c.events)
			if held != c.held {
				t.Fatalf("held = %v, want %v", held, c.held)
			}
			if held && ev.Type != InterruptType {
				t.Fatalf("holding event = %+v, want an interrupt", ev)
			}
			if got := (Attempt{Events: c.events}).Interrupted(); got != c.held {
				t.Fatalf("Attempt.Interrupted = %v, want %v", got, c.held)
			}
		})
	}
}
