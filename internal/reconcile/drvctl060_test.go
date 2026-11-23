package reconcile_test

import (
	"context"
	"slices"
	"testing"

	"github.com/Dawil/draiver/internal/agentmsg"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
)

// TestSessionSpawnCarriesIdentity (drvctl-060 fix 1): a brought-up session is
// handed DRAIVER_ACTOR=agent:<adapter> and DRAIVER_ATTEMPT=<id>, so its `draiver`
// writes are attributed to the agent without an export prefix.
func TestSessionSpawnCarriesIdentity(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	att := w.newTicket(t, "IDN")

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	waitFor(t, "spawned", func() bool { return f.count() == 1 && len(f.at(0).prompted()) > 0 })

	env := f.at(0).spawnEnv()
	for _, want := range []string{"DRAIVER_ACTOR=agent:claude-code", "DRAIVER_ATTEMPT=" + att} {
		if !slices.Contains(env, want) {
			t.Fatalf("spawn env missing %q: %v", want, env)
		}
	}
}

// TestInterruptedAttemptIsNotAdmitted (drvctl-060 fix 3): an enabled attempt whose
// log carries an `interrupt` hold is not brought up, tick after tick, until the
// human acts — here a message to the agent, which lifts the hold.
func TestInterruptedAttemptIsNotAdmitted(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "HLD"
	att := w.newTicket(t, ticket)
	appendEvt(t, w.root, ticket, att, event.Event{Type: project.InterruptType, Actor: "agent:draiver", Body: "Interrupted by human."})

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)
	for i := 0; i < 5; i++ {
		if err := r.Tick(ctx); err != nil {
			t.Fatalf("held tick: %v", err)
		}
	}
	if n := f.count(); n != 0 {
		t.Fatalf("an interrupted attempt must not be admitted, got %d sessions", n)
	}

	appendEvt(t, w.root, ticket, att, event.Event{Type: "note", Actor: "human:david", To: agentmsg.ToAgent, Body: "Carry on, but skip the refactor."})
	waitFor(t, "admitted after the human's message", func() bool {
		if err := r.Tick(ctx); err != nil {
			t.Fatalf("tick: %v", err)
		}
		return f.count() == 1
	})
}
