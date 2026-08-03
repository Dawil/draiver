package project

import (
	"testing"

	"draiver/internal/event"
)

func ev(seq int, typ string, refs ...int) event.Event {
	return event.Event{Seq: seq, Type: typ, Refs: refs}
}

func TestDerive(t *testing.T) {
	cases := []struct {
		name   string
		events []event.Event
		want   State
		open   int
	}{
		{"empty", nil, Running, 0},
		{"created only", []event.Event{ev(1, "created")}, Running, 0},
		{"working with notes", []event.Event{ev(1, "created"), ev(2, "gotcha"), ev(3, "decision")}, Running, 0},
		{"open escalation", []event.Event{ev(1, "created"), ev(2, "escalation")}, NeedsMe, 1},
		{"resolved escalation returns to running", []event.Event{ev(1, "created"), ev(2, "escalation"), ev(3, "resolution", 2)}, Running, 0},
		{"review claim", []event.Event{ev(1, "created"), ev(2, "review")}, Review, 0},
		{"done is terminal", []event.Event{ev(1, "created"), ev(2, "review"), ev(3, "done")}, Done, 0},
		{"open escalation outranks review", []event.Event{ev(1, "created"), ev(2, "review"), ev(3, "escalation")}, NeedsMe, 1},
		{"reopened after resolution", []event.Event{ev(1, "created"), ev(2, "escalation"), ev(3, "resolution", 2), ev(4, "escalation")}, NeedsMe, 1},
		{"two escalations one resolved", []event.Event{ev(1, "escalation"), ev(2, "escalation"), ev(3, "resolution", 1)}, NeedsMe, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, open := Derive(c.events)
			if got != c.want {
				t.Errorf("state = %q want %q", got, c.want)
			}
			if len(open) != c.open {
				t.Errorf("open escalations = %d want %d", len(open), c.open)
			}
		})
	}
}
