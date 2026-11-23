package reconcile_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/agentmsg"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// TestHumanEventsPushedToLiveSession (drvctl-062): a human decision and then a
// human supersede of it, appended while a session is live, each reach the session
// as one injected turn on the next tick; agent-authored and spine events are never
// echoed, and nothing is pushed twice.
func TestHumanEventsPushedToLiveSession(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PUSH"
	att := w.newTicket(t, ticket)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "briefed", func() bool { return f.count() == 1 && len(f.at(0).prompted()) == 1 })
	a := f.at(0)

	app := func(e event.Event) int {
		t.Helper()
		got, err := ticketlog.Append(w.root, ticket, att, e)
		if err != nil {
			t.Fatal(err)
		}
		return got.Seq
	}
	app(event.Event{Type: "decision", Actor: "agent:claude-code", Body: "agent's own decision"})
	app(event.Event{Type: "note", Actor: "human:ctl", Body: "Restart flush: spine bookkeeping"})
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(a.prompted()); n != 1 {
		t.Fatalf("agent/spine events must not be pushed: %d prompts: %q", n, a.prompted())
	}

	dec := app(event.Event{Type: "decision", Actor: "human:david", Body: "Build the CSV export"})
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if p := a.prompted(); len(p) != 2 || !strings.Contains(p[1], "Build the CSV export") {
		t.Fatalf("human decision must be pushed as a turn: %q", p)
	}

	app(event.Event{Type: "note", Actor: "human:david", Supersedes: []int{dec}, Body: "ignore that — it was for another ticket"})
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	p := a.prompted()
	if len(p) != 3 || !strings.Contains(p[2], "supersede (retires #") || !strings.Contains(p[2], "ignore that") || strings.Contains(p[2], "Build the CSV export") {
		t.Fatalf("supersede must be pushed alone as a turn: %q", p)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(a.prompted()); n != 3 {
		t.Fatalf("nothing new ⇒ no push: %d", n)
	}
}

// TestResumeNoteCarriesHumanDeltaCapped (drvctl-062): the resume note lists the
// human events the previous session never read — including an unmarked human
// supersede below the agent's last event — newest first kept, older ones counted.
func TestResumeNoteCarriesHumanDeltaCapped(t *testing.T) {
	w := newWorld(t)
	ticket := "DELTA"
	att := w.newTicket(t, ticket)
	evs, _ := ticketlog.Read(w.root, ticket, att)
	if err := agentmsg.WriteWatermark(w.root, ticket, att, evs[len(evs)-1].Seq); err != nil {
		t.Fatal(err)
	}
	dec, _ := ticketlog.Append(w.root, ticket, att, event.Event{Type: "decision", Actor: "human:david", Body: "Build the CSV export"})
	sup, _ := ticketlog.Append(w.root, ticket, att, event.Event{Type: "note", Actor: "human:david", Supersedes: []int{dec.Seq}, Body: "ignore that — wrong ticket"})
	appendEvt(t, w.root, ticket, att, event.Event{Type: "decision", Actor: "agent:claude-code", Body: "agent decision after the supersede"})

	note := reconcile.ResumeWakeNote(w.root, ticket, att)
	if !strings.Contains(note, fmt.Sprintf("#%d — ignore that", sup.Seq)) || !strings.Contains(note, fmt.Sprintf("superseding [%d]", dec.Seq)) || !strings.Contains(note, "from the human, never read") {
		t.Fatalf("resume note must carry the unread supersede:\n%s", note)
	}

	for i := 0; i < 12; i++ {
		appendEvt(t, w.root, ticket, att, event.Event{Type: "decision", Actor: "human:david", Body: fmt.Sprintf("later decision %02d", i)})
	}
	note = reconcile.ResumeWakeNote(w.root, ticket, att)
	if !strings.Contains(note, "earlier event(s) not listed") || !strings.Contains(note, "later decision 11") || strings.Contains(note, "Build the CSV export") {
		t.Fatalf("resume note must be capped to the newest events:\n%s", note)
	}
}
