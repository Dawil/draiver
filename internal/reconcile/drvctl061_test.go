package reconcile_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/handoff"
	"github.com/Dawil/draiver/internal/limit"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// reconcilerHandoff wires a Reconciler with the drvctl-061 checkpoint enabled.
func (w world) reconcilerHandoff(t *testing.T, f *factory, p *fakeProc, contextLimit, autoRestarts int, grace time.Duration) *reconcile.Reconciler {
	t.Helper()
	r, err := reconcile.New(reconcile.Options{
		Root:                w.root,
		Adapters:            f.adapters,
		Actor:               "agent:claude-code",
		ContextLimit:        contextLimit,
		StopGrace:           grace,
		ContextAutoRestarts: autoRestarts,
		HandoffPoll:         5 * time.Millisecond,
		Proc:                p,
	})
	if err != nil {
		t.Fatalf("reconcile.New: %v", err)
	}
	return r
}

func handoffPrompted(a *fakeAdapter) bool {
	return slices.ContainsFunc(a.prompted(), func(p string) bool { return strings.Contains(p, "hand off now") })
}

// writeHandoffNote is the fake session running
// `draiver log <ticket> --type note --tag handoff "…"`.
func writeHandoffNote(t *testing.T, w world, ticket, att, body string) {
	t.Helper()
	if _, err := ticketlog.Append(w.root, ticket, att, event.Event{
		Type: "note", Actor: "agent:claude-code", Tags: []string{handoff.Tag}, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
}

func countTaggedEvents(t *testing.T, w world, ticket, att, typ, tag string) int {
	t.Helper()
	evs, err := ticketlog.Read(w.root, ticket, att)
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

// TestAutoStopHandoffRestartsOnceThenEscalates is the drvctl-061 headline arc: a
// session crossing the context limit is asked for a handoff note before the reap;
// once it logs one, the supervisor resolves its own escalation and the next tick
// brings the attempt back on a fresh session whose brief carries the note. The
// same attempt tripping again escalates to a human.
func TestAutoStopHandoffRestartsOnceThenEscalates(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)
	f := &factory{}
	r := w.reconcilerHandoff(t, f, newProc(), 150_000, 1, 3*time.Second)
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	s1 := f.at(0)
	waitFor(t, "brief prompt", func() bool { return len(s1.prompted()) > 0 })

	s1.emit(usageEvt(151_000))
	waitFor(t, "handoff prompt", func() bool { return handoffPrompted(s1) })
	if s1.wasKilled() {
		t.Fatal("the session must stay live while it writes its handoff")
	}
	if countTaggedEvents(t, w, ticket, att, "escalation", limit.TagContextAutoStop) != 1 {
		t.Fatal("the auto-stop escalation must be durable before the handoff")
	}
	// A tick mid-checkpoint (attempt is Needs-me) must not retire the live session.
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if s1.wasKilled() {
		t.Fatal("a tick retired the session mid-handoff")
	}

	writeHandoffNote(t, w, ticket, att, "Handoff: parser half done, next wire the CLI")
	waitFor(t, "session reaped after the note", s1.wasKilled)
	waitFor(t, "supervisor resolution", func() bool {
		return countTaggedEvents(t, w, ticket, att, "resolution", limit.TagContextAutoRestart) == 1
	})

	// The next tick re-admits on a fresh session cold-started from the brief.
	waitFor(t, "fresh session admitted", func() bool {
		_ = r.Tick(ctx)
		return f.count() == 2
	})
	s2 := f.at(1)
	if s2.resumedWith() != "" {
		t.Fatalf("restart resumed %q; want a fresh spawn", s2.resumedWith())
	}
	waitFor(t, "fresh brief", func() bool { return len(s2.prompted()) > 0 })
	if !strings.Contains(s2.prompted()[0], "parser half done") {
		t.Fatalf("the handoff note is missing from the fresh session's brief:\n%s", s2.prompted()[0])
	}

	// Second trip: the budget is spent, so the escalation stays open.
	s2.emit(usageEvt(152_000))
	waitFor(t, "second handoff prompt", func() bool { return handoffPrompted(s2) })
	writeHandoffNote(t, w, ticket, att, "Handoff: CLI wired, tests left")
	waitFor(t, "second reap", s2.wasKilled)
	a, err := project.LoadAttempt(w.root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != project.NeedsMe {
		t.Fatalf("state = %v, want NeedsMe after the second trip", a.State)
	}
	if n := countTaggedEvents(t, w, ticket, att, "resolution", limit.TagContextAutoRestart); n != 1 {
		t.Fatalf("auto-restart resolutions = %d, want still 1", n)
	}
	_ = r.Tick(ctx)
	time.Sleep(50 * time.Millisecond)
	if f.count() != 2 {
		t.Fatalf("sessions = %d; a second trip must wait for a human", f.count())
	}
}

// TestAutoStopHandoffTimeoutLeavesForHuman: no note within the grace period → the
// session is reaped, the escalation stays open, nothing restarts.
func TestAutoStopHandoffTimeoutLeavesForHuman(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)
	f := &factory{}
	r := w.reconcilerHandoff(t, f, newProc(), 150_000, 1, 100*time.Millisecond)
	t.Cleanup(r.Close)

	_ = r.Tick(ctx)
	s1 := f.at(0)
	waitFor(t, "brief prompt", func() bool { return len(s1.prompted()) > 0 })
	s1.emit(usageEvt(151_000))
	waitFor(t, "reaped after grace", s1.wasKilled)
	if countTaggedEvents(t, w, ticket, att, "resolution", limit.TagContextAutoRestart) != 0 {
		t.Fatal("a timed-out handoff must not auto-restart")
	}
	_ = r.Tick(ctx)
	if f.count() != 1 {
		t.Fatalf("sessions = %d, want 1", f.count())
	}
}

// TestCtlStopHandsOffThroughOwningDaemon: `ctl stop` with a live `ctl up` hands the
// stop to it; the daemon prompts the session for a handoff note, then reaps it and
// holds the attempt so it is not re-admitted.
func TestCtlStopHandsOffThroughOwningDaemon(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)
	f := &factory{}
	p := newProc()
	daemon := w.reconcilerHandoff(t, f, p, 0, 1, 3*time.Second)
	t.Cleanup(daemon.Close)
	ctrl := reconcile.Controller{PID: 77001, Nonce: "boot-1", Started: time.Now()}
	if err := reconcile.WriteController(w.root, ctrl); err != nil {
		t.Fatal(err)
	}
	p.setAlive(ctrl.PID, true)

	_ = daemon.Tick(ctx)
	s1 := f.at(0)
	waitFor(t, "brief prompt", func() bool { return len(s1.prompted()) > 0 })
	s1.emit(bashCall("t1", "go test ./...")) // a turn in flight

	client := w.reconcilerHandoff(t, &factory{}, p, 0, 1, 3*time.Second)
	type out struct {
		res reconcile.StopResult
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := client.StopWithHandoff(ticket, att, false)
		done <- out{res, err}
	}()

	waitFor(t, "daemon prompts for the handoff", func() bool {
		_ = daemon.Tick(ctx)
		return handoffPrompted(s1)
	})
	if s1.wasKilled() {
		t.Fatal("reaped before the handoff")
	}
	writeHandoffNote(t, w, ticket, att, "Handoff: stopped by human mid-test")

	var got out
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctl stop did not return")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !got.res.Handoff || got.res.NoteSeq == 0 || got.res.Outcome != string(handoff.Written) || got.res.Direct {
		t.Fatalf("stop result = %+v, want a written handoff", got.res)
	}
	if !s1.wasKilled() {
		t.Fatal("session not reaped")
	}
	_ = daemon.Tick(ctx)
	time.Sleep(50 * time.Millisecond)
	_ = daemon.Tick(ctx)
	if f.count() != 1 {
		t.Fatalf("sessions = %d; a stopped attempt must not be re-admitted", f.count())
	}
}

// TestCtlStopWithoutDaemonReapsDirectly: no `ctl up` → today's direct reap, said so.
// --now never asks for a handoff.
func TestCtlStopWithoutDaemonReapsDirectly(t *testing.T) {
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)
	r := w.reconcilerHandoff(t, &factory{}, newProc(), 0, 1, time.Second)

	res, err := r.StopWithHandoff(ticket, att, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Direct || res.Handoff || !strings.Contains(res.Note, "no running `ctl up`") {
		t.Fatalf("result = %+v, want a direct reap that says why", res)
	}

	res, err = r.StopWithHandoff(ticket, att, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Direct || res.Handoff || res.Note != "" {
		t.Fatalf("--now result = %+v, want today's plain stop", res)
	}
}
