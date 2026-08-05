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

// TestRestartResumesWithoutRebrief closes the new restart contract: with no
// teardown depth selected, Restart reaps the process and the start cascade Resumes
// the SAME session id (a continue, not a new attempt) — and, because the session
// layer (L0) was kept, does NOT re-inject the cold-start brief. This is the clean
// "continue" the old hybrid restart failed to be (it resumed the same id yet
// force-fed a brief); the brief is coupled to a reset, not to restart (drvctl-016).
func TestRestartResumesWithoutRebrief(t *testing.T) {
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

	col := newCollector()
	done := make(chan error, 1)
	go func() { done <- r.Restart(ctx, ticket, att, col.observe) }()

	// Restart resumes on the recorded id, not a fresh Spawn.
	waitFor(t, "resume", func() bool { return f.count() == 2 })
	resumed := f.at(1)
	waitFor(t, "resume on the surviving id", func() bool { return resumed.resumedWith() == orig.SessionID })

	// Once the resumed stream is being consumed we are past the point a fresh spawn
	// would have briefed; a resume must never have been prompted with a brief.
	waitFor(t, "resumed stream consumed", func() bool { return col.count(agent.EventSystem) >= 1 })
	if p := resumed.prompted(); len(p) != 0 {
		t.Fatalf("a resume must not be re-briefed, but was prompted: %v", p)
	}

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

// TestBringUpFallsThroughWhenResumeNeverComesOnline is the cascade's self-heal: a
// recorded session id that can no longer be resumed launches a process that never
// comes online, so bringUp reaps it and falls through to a fresh Spawn on the same
// worktree — retiring the "stale id resumed forever / manual rm -rf" failure. And
// because the fall-through discarded L0, the fresh session is cold-started from the
// brief.
func TestBringUpFallsThroughWhenResumeNeverComesOnline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	// Seed a session id on record so bringUp attempts a Resume first.
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	if err := sess.WriteIdentity(session.Identity{Adapter: "claude-code", SessionID: "stale-id"}); err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	f := &factory{staleResume: true}
	r := w.reconcilerConfirm(t, f, newProc(), 50*time.Millisecond)
	t.Cleanup(r.Close)

	done := make(chan error, 1)
	go func() { done <- r.Start(ctx, ticket, att, nil) }()

	// The recorded id is Resumed first (fake #0) but never comes online; the cascade
	// reaps it and Spawns fresh (fake #1) on the same worktree.
	waitFor(t, "resume then fresh spawn", func() bool { return f.count() == 2 })
	resumeTry, fresh := f.at(0), f.at(1)
	if resumeTry.resumedWith() != "stale-id" {
		t.Fatalf("first bring-up should Resume the recorded id, got %q", resumeTry.resumedWith())
	}
	if fresh.resumedWith() != "" {
		t.Fatalf("fall-through should be a fresh Spawn, not a Resume (got resume id %q)", fresh.resumedWith())
	}
	waitFor(t, "stale resume reaped", func() bool { return resumeTry.wasKilled() })

	// The fresh session cold-starts from the brief (L0 was discarded)...
	waitFor(t, "brief on fresh spawn", func() bool {
		p := fresh.prompted()
		return len(p) > 0 && strings.Contains(p[0], "BRIEF")
	})
	// ...and session.json now holds the fresh id, not the dead one.
	waitFor(t, "session id replaced", func() bool {
		id, err := sess.ReadIdentity()
		return err == nil && id.SessionID == "sess-live"
	})

	cancel()
	<-done
}
