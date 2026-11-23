package agentmsg

import (
	"slices"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
)

// drv-044 shape: the human logs a decision meant for another ticket and then a
// note retracting it, while the agent keeps logging its own events.
func drv044Log() []event.Event {
	return []event.Event{
		{Seq: 10, Type: "decision", Actor: "agent:claude-code", Body: "Start on export"},
		{Seq: 11, Type: "note", Actor: "human:david", Body: "Daemon kept the worktree warm"}, // spine
		{Seq: 12, Type: "decision", Actor: "human:david", Body: "Add a CSV button\nlong detail"},
		{Seq: 13, Type: "gotcha", Actor: "agent:claude-code", Supersedes: []int{10}, Body: "agent retires its own fact"},
		{Seq: 14, Type: "note", Actor: "human:david", Supersedes: []int{12}, Body: "ignore #12 — wrong ticket"},
		{Seq: 15, Type: "resolution", Actor: "human:webui", Body: "Use the staging DB"},
		{Seq: 16, Type: "gotcha", Actor: "human:david", Body: "unmarked human gotcha"},
	}
}

func TestIsDeliverable(t *testing.T) {
	want := map[int]bool{10: false, 11: false, 12: true, 13: false, 14: true, 15: true, 16: false}
	for _, e := range drv044Log() {
		if got := IsDeliverable(e); got != want[e.Seq] {
			t.Errorf("#%d %s by %s: IsDeliverable = %v, want %v", e.Seq, e.Type, e.Actor, got, want[e.Seq])
		}
	}
	if !IsDeliverable(event.Event{Type: "gotcha", Actor: "agent:x", To: ToAgent}) {
		t.Error("an explicit message must stay deliverable regardless of type/actor")
	}
}

func seqs(es []event.Event) []int {
	var out []int
	for _, e := range es {
		out = append(out, e.Seq)
	}
	return out
}

// TestSeenBaseline: the read side's floor is max(session watermark, read cursor);
// the push side's floor is the push cursor alone. Neither re-lists an event at or
// below its floor, and both skip agent-authored and spine events.
func TestSeenBaseline(t *testing.T) {
	evs := drv044Log()
	cases := []struct {
		name              string
		watermark, cursor int
		want              []int
	}{
		{"session started before everything", 0, 0, []int{12, 14, 15}},
		{"session started after the decision", 12, 0, []int{14, 15}},
		{"cursor ahead of watermark", 10, 14, []int{15}},
		{"watermark ahead of cursor", 14, 12, []int{15}},
		{"all read", 10, 15, nil},
	}
	for _, c := range cases {
		if got := seqs(Unread(evs, c.watermark, c.cursor)); !slices.Equal(got, c.want) {
			t.Errorf("%s: Unread = %v, want %v", c.name, got, c.want)
		}
	}
	if got := seqs(After(evs, 13)); !slices.Equal(got, []int{14, 15}) {
		t.Errorf("After(13) = %v", got)
	}
}

func TestWarningNamesEventsAndCaps(t *testing.T) {
	evs := After(drv044Log(), 0)
	w := Warning(evs)
	for _, want := range []string{"3 new human events since your last read: #12 decision; #14 supersede (retires #12); #15 resolution", "draiver messages"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning missing %q: %s", want, w)
		}
	}
	if strings.Contains(w, "CSV") || strings.Contains(w, "wrong ticket") {
		t.Errorf("warning must carry no content: %s", w)
	}
	many := append(append([]event.Event{}, evs...), evs...)
	if w := Warning(many); !strings.Contains(w, "+3 more") {
		t.Errorf("warning must cap the list: %s", w)
	}
}

func TestPushTextQuotesBodiesAndCaps(t *testing.T) {
	evs := After(drv044Log(), 13)
	p := PushText("T-1", evs)
	for _, want := range []string{"2 events", "#14 supersede (retires #12) · human:david", "ignore #12 — wrong ticket", "Use the staging DB", "draiver messages"} {
		if !strings.Contains(p, want) {
			t.Errorf("push text missing %q:\n%s", want, p)
		}
	}
	long := event.Event{Seq: 20, Type: "decision", Actor: "human:x", Body: strings.Repeat("a", pushBodyCap+50)}
	if p := PushText("T-1", []event.Event{long}); !strings.Contains(p, "truncated — `draiver recall T-1 20`") || strings.Contains(p, strings.Repeat("a", pushBodyCap+1)) {
		t.Errorf("long body must be capped")
	}
}
