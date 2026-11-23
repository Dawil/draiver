package project

import "github.com/Dawil/draiver/internal/event"

// Interrupt hold (drvctl-060). When the human interrupts a supervised session —
// Claude Code records "[Request interrupted by user]", or they `ctl stop` an
// enabled attempt — the supervisor appends an `interrupt` event. Until the human
// acts again the attempt is *held*: the reconciler does not re-admit it, so the
// session is not auto-resumed over the human (drv-044: resumed ~10s after each of
// two interrupts until the human disabled it). The board shows it Pending,
// "interrupted by human".
//
// The hold is a pure projection of the log, like DeriveEnabled: no view state, and
// it replays from disk.
const (
	// InterruptType is the event type recording a human interrupt.
	InterruptType = "interrupt"
	// StartType is the event `ctl start` records when it lifts an interrupt hold.
	StartType = "start"
	// InterruptedReason is the board's Pending line for a held attempt.
	InterruptedReason = "interrupted by human"
)

// DeriveInterrupted reports whether an attempt is held by a human interrupt, and the
// interrupt event holding it. The latest `interrupt` holds until a later event that
// is a human act directed at the attempt:
//
//   - a message to the agent (an event with `to: agent`, drvctl-059);
//   - a `resolution` (answering an escalation);
//   - an `enable` (re-opting it into supervision);
//   - a `start` (`ctl start` on the held attempt).
//
// Anything else — daemon notes, retire bookkeeping, a test result landing — leaves
// the hold in place.
func DeriveInterrupted(events []event.Event) (bool, event.Event) {
	var held *event.Event
	for i := range events {
		e := events[i]
		switch {
		case e.Type == InterruptType:
			held = &events[i]
		case held != nil && liftsInterrupt(e):
			held = nil
		}
	}
	if held == nil {
		return false, event.Event{}
	}
	return true, *held
}

// liftsInterrupt reports whether e is a human act that releases an interrupt hold.
func liftsInterrupt(e event.Event) bool {
	if e.To == "agent" {
		return true
	}
	switch e.Type {
	case "resolution", "enable", StartType:
		return true
	}
	return false
}

// Interrupted is DeriveInterrupted over the attempt's loaded events.
func (a Attempt) Interrupted() bool {
	held, _ := DeriveInterrupted(a.Events)
	return held
}
