package limit_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/handoff"
	"github.com/Dawil/draiver/internal/limit"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// trace records, in order, what the gate did to the session and the log, so the
// drvctl-061 ordering contract can be asserted directly.
type trace struct {
	mu    sync.Mutex
	steps []string
}

func (tr *trace) add(s string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.steps = append(tr.steps, s)
}

func (tr *trace) get() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]string(nil), tr.steps...)
}

// agentSession is a fake live session: on Prompt it records whether the escalation
// was already durable, then (when writeNote) logs a handoff note as the session
// actor would through `draiver log`.
type agentSession struct {
	t         *testing.T
	root      store.Root
	ticket    string
	att       string
	tr        *trace
	writeNote bool
}

func (s *agentSession) Interrupt(context.Context) error { s.tr.add("interrupt"); return nil }

func (s *agentSession) Prompt(context.Context, string) error {
	evs, _ := ticketlog.Read(s.root, s.ticket, s.att)
	if slices.ContainsFunc(evs, func(e event.Event) bool { return e.Type == "escalation" }) {
		s.tr.add("prompt(escalation durable)")
	} else {
		s.tr.add("prompt(NO escalation yet)")
	}
	if s.writeNote {
		go func() {
			if _, err := ticketlog.Append(s.root, s.ticket, s.att, event.Event{
				Type: "note", Actor: "agent:claude-code", Tags: []string{handoff.Tag}, Body: "Handoff: done X",
			}); err != nil {
				s.t.Error(err)
			}
		}()
	}
	return nil
}

func handoffGate(t *testing.T, root store.Root, ticket, att string, tr *trace, writeNote bool, restarts int, grace time.Duration, reports chan<- limit.Report) *limit.Gate {
	sess := &agentSession{t: t, root: root, ticket: ticket, att: att, tr: tr, writeNote: writeNote}
	return limit.New(root, ticket, att, "agent:claude-code", 100_000, func() error {
		tr.add("halt")
		return nil
	}).WithHandoff(limit.Handoff{
		Grace:        grace,
		Session:      sess,
		AutoRestarts: restarts,
		Restart:      func() error { tr.add("restart-flush"); return nil },
		Report:       func(r limit.Report) { reports <- r },
		Poll:         5 * time.Millisecond,
	})
}

func countTagged(t *testing.T, root store.Root, ticket, att, typ, tag string) int {
	t.Helper()
	evs, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Type == typ && slices.Contains(e.Tags, tag) {
			n++
		}
	}
	return n
}

// TestHandoffOrderingAndRestart: escalation durable → interrupt → prompt → note →
// supervisor resolution + session flush → halt. The attempt is Running again (the
// escalation resolved) so the reconciler re-admits it fresh.
func TestHandoffOrderingAndRestart(t *testing.T) {
	root, ticket, att := newFixture(t)
	tr := &trace{}
	reports := make(chan limit.Report, 1)
	g := handoffGate(t, root, ticket, att, tr, true, 1, 2*time.Second, reports)

	out, err := g.Consider(usageEvent(120_000)) // a mid-turn frame
	if err != nil || out != limit.Checkpointing {
		t.Fatalf("crossing: out=%v err=%v, want Checkpointing", out, err)
	}
	// While checkpointing, later frames are fed to the checkpoint, not re-judged.
	if out, _ := g.Consider(usageEvent(130_000)); out != limit.Checkpointing && out != limit.Ignored {
		t.Fatalf("later frame out=%v", out)
	}
	rep := <-reports
	g.Wait()
	if g.Checkpointing() {
		t.Fatal("still checkpointing after the reap")
	}

	want := []string{"interrupt", "prompt(escalation durable)", "restart-flush", "halt"}
	if got := tr.get(); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if rep.Result.Outcome != handoff.Written || !rep.Restarted || rep.Restarts != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if n := countTagged(t, root, ticket, att, "resolution", limit.TagContextAutoRestart); n != 1 {
		t.Fatalf("auto-restart resolutions = %d, want 1", n)
	}
	evs, _ := ticketlog.Read(root, ticket, att)
	if st, open := project.Derive(evs); st != project.Running || len(open) != 0 {
		t.Fatalf("state = %v open=%d, want Running with nothing open", st, len(open))
	}
	if out, _ := g.Consider(usageEvent(150_000)); out != limit.Ignored {
		t.Fatalf("after the reap the gate stays latched: out=%v", out)
	}
}

// TestSecondTripEscalatesToHuman: the restart budget is counted from the durable
// log, so the same attempt tripping again (a new session, a new gate) leaves the
// escalation open for a human.
func TestSecondTripEscalatesToHuman(t *testing.T) {
	root, ticket, att := newFixture(t)
	reports := make(chan limit.Report, 1)

	g1 := handoffGate(t, root, ticket, att, &trace{}, true, 1, 2*time.Second, reports)
	_, _ = g1.Consider(usageEvent(120_000))
	if rep := <-reports; !rep.Restarted {
		t.Fatalf("first trip should restart: %+v", rep)
	}
	g1.Wait()

	tr := &trace{}
	g2 := handoffGate(t, root, ticket, att, tr, true, 1, 2*time.Second, reports)
	_, _ = g2.Consider(agent.Event{Kind: agent.EventTurnEnd, Turn: "success", Usage: &agent.Usage{ContextTokens: 101_000}})
	rep := <-reports
	g2.Wait()
	if rep.Restarted || rep.Restarts != 1 || rep.Result.Outcome != handoff.Written {
		t.Fatalf("second trip report = %+v, want written but not restarted (budget 1 spent)", rep)
	}
	// A turn-end crossing has no turn in flight: no interrupt, and no flush.
	if want := []string{"prompt(escalation durable)", "halt"}; !slices.Equal(tr.get(), want) {
		t.Fatalf("order = %v, want %v", tr.get(), want)
	}
	evs, _ := ticketlog.Read(root, ticket, att)
	if st, open := project.Derive(evs); st != project.NeedsMe || len(open) != 1 {
		t.Fatalf("state = %v open=%d, want NeedsMe with the second escalation open", st, len(open))
	}
}

// TestHandoffTimeoutStillHaltsAndLeavesEscalation: no note within grace → no
// resolution, no flush, the session is reaped anyway and a human resolves.
func TestHandoffTimeoutStillHaltsAndLeavesEscalation(t *testing.T) {
	root, ticket, att := newFixture(t)
	tr := &trace{}
	reports := make(chan limit.Report, 1)
	g := handoffGate(t, root, ticket, att, tr, false, 1, 60*time.Millisecond, reports)
	_, _ = g.Consider(usageEvent(120_000))
	rep := <-reports
	g.Wait()
	if rep.Result.Outcome != handoff.TimedOut || rep.Restarted {
		t.Fatalf("report = %+v, want timed-out, not restarted", rep)
	}
	if got := tr.get(); got[len(got)-1] != "halt" || slices.Contains(got, "restart-flush") {
		t.Fatalf("order = %v, want a halt and no flush", got)
	}
	evs, _ := ticketlog.Read(root, ticket, att)
	if st, _ := project.Derive(evs); st != project.NeedsMe {
		t.Fatalf("state = %v, want NeedsMe", st)
	}
}

// TestZeroAutoRestartsNeverResolves: context_auto_restarts 0 still checkpoints but
// always leaves the escalation for a human.
func TestZeroAutoRestartsNeverResolves(t *testing.T) {
	root, ticket, att := newFixture(t)
	reports := make(chan limit.Report, 1)
	g := handoffGate(t, root, ticket, att, &trace{}, true, 0, 2*time.Second, reports)
	_, _ = g.Consider(usageEvent(120_000))
	if rep := <-reports; rep.Restarted || rep.Result.Outcome != handoff.Written {
		t.Fatalf("report = %+v", rep)
	}
	if n := countTagged(t, root, ticket, att, "resolution", limit.TagContextAutoRestart); n != 0 {
		t.Fatalf("resolutions = %d, want 0", n)
	}
}

// TestHaltFailureAfterHandoffIsReported: the escalation and the handoff are durable
// even when the reap itself fails.
func TestHaltFailureAfterHandoffIsReported(t *testing.T) {
	root, ticket, att := newFixture(t)
	reports := make(chan limit.Report, 1)
	sess := &agentSession{t: t, root: root, ticket: ticket, att: att, tr: &trace{}, writeNote: true}
	g := limit.New(root, ticket, att, "agent:claude-code", 100_000, func() error { return errors.New("kill failed") }).
		WithHandoff(limit.Handoff{Grace: 2 * time.Second, Session: sess, Report: func(r limit.Report) { reports <- r }, Poll: 5 * time.Millisecond})
	_, _ = g.Consider(usageEvent(120_000))
	rep := <-reports
	if rep.HaltErr == nil {
		t.Fatal("halt error not reported")
	}
	if countTagged(t, root, ticket, att, "escalation", limit.TagContextAutoStop) != 1 ||
		countTagged(t, root, ticket, att, "note", handoff.Tag) != 1 {
		t.Fatal("escalation and handoff note must both be durable despite the failed reap")
	}
}

// TestZeroGraceIsTodaysBehaviour: a zero grace disables the checkpoint — the gate
// halts synchronously and reports Stopped, with the pre-drvctl-061 body.
func TestZeroGraceIsTodaysBehaviour(t *testing.T) {
	root, ticket, att := newFixture(t)
	tr := &trace{}
	g := limit.New(root, ticket, att, "agent:claude-code", 100_000, func() error { tr.add("halt"); return nil }).
		WithHandoff(limit.Handoff{Grace: 0, Session: &agentSession{tr: tr}, AutoRestarts: 1})
	if out, err := g.Consider(usageEvent(120_000)); err != nil || out != limit.Stopped {
		t.Fatalf("out=%v err=%v, want Stopped", out, err)
	}
	if got := tr.get(); !slices.Equal(got, []string{"halt"}) {
		t.Fatalf("steps = %v, want only the halt", got)
	}
	evs, _ := ticketlog.Read(root, ticket, att)
	for _, e := range evs {
		if e.Type == "escalation" && strings.Contains(e.Body, "handoff") {
			t.Fatalf("zero-grace escalation body mentions a handoff: %q", e.Body)
		}
	}
}
