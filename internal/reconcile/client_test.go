package reconcile_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/session"
)

// collector accumulates the events a foreground Start mirrors to its observer, so
// a test can assert the stream was rendered.
type collector struct {
	mu   sync.Mutex
	kind map[agent.EventKind]int
}

func newCollector() *collector { return &collector{kind: map[agent.EventKind]int{}} }

func (c *collector) observe(ev agent.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kind[ev.Kind]++
}

func (c *collector) count(k agent.EventKind) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kind[k]
}

// TestStartForegroundBringsUpAndDispatches: `ctl start` on one attempt spawns a
// session, injects the cold-start brief, and runs the same watch+gate+protocol
// pipeline the daemon does — a promoted gotcha lands in the durable log and a
// usage frame is metered. Ctrl-C (ctx cancel) reaps the session but keeps its id
// for a later resume.
func TestStartForegroundBringsUpAndDispatches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	col := newCollector()
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx, ticket, att, col.observe) }()

	// A session came up and the brief was injected as the first prompt.
	waitFor(t, "spawn", func() bool { return f.count() == 1 })
	live := f.at(0)
	waitFor(t, "brief prompt", func() bool {
		p := live.prompted()
		return len(p) > 0 && strings.Contains(p[0], "BRIEF")
	})

	// The agent's `draiver log` is promoted, and a usage frame is metered — the
	// full pipeline is live under the foreground driver.
	live.emit(bashCall("t1", `draiver log PROJ-1 --type gotcha "hand-driven session works"`))
	waitFor(t, "promoted gotcha", func() bool {
		return hasType(logTypes(t, w.root, ticket, att), "gotcha")
	})
	live.emit(agent.Event{Kind: agent.EventUsage, Usage: &agent.Usage{ContextTokens: 4242, CostUSD: 0.12}, Raw: json.RawMessage(`{"u":1}`)})

	sess, err := session.Open(w.root, ticket, att)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	waitFor(t, "metered usage", func() bool {
		m, err := sess.ReadMeter()
		return err == nil && m.Usage.ContextTokens == 4242
	})

	// The observer saw the live stream rendered.
	if col.count(agent.EventToolCall) == 0 {
		t.Fatal("observer never saw the tool call")
	}

	// Ctrl-C stops the foreground session; Start returns and the pid is cleared,
	// but the session id survives for a resume.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}
	id, err := sess.ReadIdentity()
	if err != nil {
		t.Fatalf("read identity: %v", err)
	}
	if id.PID != 0 {
		t.Fatalf("pid should be cleared after a foreground stop, got %d", id.PID)
	}
	if id.SessionID == "" {
		t.Fatal("session id must survive for a later resume")
	}
	if !live.wasKilled() {
		t.Fatal("the session process should have been reaped")
	}
}

// TestStartExitsWhenSessionEnds: a session that exits on its own (its stream
// closes) makes a foreground Start return without needing a cancel.
func TestStartExitsWhenSessionEnds(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	done := make(chan error, 1)
	go func() { done <- r.Start(ctx, ticket, att, nil) }()

	waitFor(t, "spawn", func() bool { return f.count() == 1 })
	live := f.at(0)
	waitFor(t, "brief prompt", func() bool { return len(live.prompted()) > 0 })

	// The agent process exits — its stream closes.
	if err := live.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after the session exited")
	}
}

// TestStopSignalsAndClearsPid: Stop terminates the recorded pid and clears it
// from session.json while keeping the cattle handle (session id) for resume.
func TestStopSignalsAndClearsPid(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	// Admit brings up a session with a live pid on record.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	id, _ := sess.ReadIdentity()
	proc.setAlive(id.PID, true)

	res, err := r.Stop(ticket, att)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !res.Signaled || res.PID != id.PID {
		t.Fatalf("expected to signal pid %d, got %+v", id.PID, res)
	}
	proc.mu.Lock()
	terminated := append([]int(nil), proc.terminated...)
	proc.mu.Unlock()
	if len(terminated) != 1 || terminated[0] != id.PID {
		t.Fatalf("pid %d not terminated, got %v", id.PID, terminated)
	}

	after, _ := sess.ReadIdentity()
	if after.PID != 0 {
		t.Fatalf("pid should be cleared after Stop, got %d", after.PID)
	}
	if after.SessionID != id.SessionID {
		t.Fatalf("session id must survive Stop: got %q want %q", after.SessionID, id.SessionID)
	}
}

// TestStopWhenAlreadyStopped: Stop on an attempt with a dead pid (or never
// started) reports already-stopped and does not error — the goal state holds.
func TestStopWhenAlreadyStopped(t *testing.T) {
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	// No session was ever started for this attempt.
	res, err := r.Stop(ticket, att)
	if err != nil {
		t.Fatalf("Stop on a never-started attempt should not error: %v", err)
	}
	if res.Signaled || !res.AlreadyStopped {
		t.Fatalf("expected already-stopped, got %+v", res)
	}
}

// TestRestartResumesFromBrief closes the restart contract: after a session exists,
// Restart reaps it and brings it back up on the SAME session id (a resume, not a
// new attempt), re-injecting the cold-start brief.
func TestRestartResumesFromBrief(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	// First bring-up records a session id (and a live pid Stop will signal).
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	orig, _ := sess.ReadIdentity()
	proc.setAlive(orig.PID, true)

	// Detach the daemon's ingest so the admitted run isn't racing the restart.
	r.Close()

	done := make(chan error, 1)
	go func() { done <- r.Restart(ctx, ticket, att, nil) }()

	// Restart resumes on the recorded id, not a fresh Spawn.
	waitFor(t, "resume", func() bool { return f.count() == 2 })
	resumed := f.at(1)
	waitFor(t, "resume on the surviving id", func() bool { return resumed.resumedWith() == orig.SessionID })
	waitFor(t, "fresh brief", func() bool {
		p := resumed.prompted()
		return len(p) > 0 && strings.Contains(p[0], "BRIEF")
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Restart returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Restart did not return after ctx cancel")
	}
}
