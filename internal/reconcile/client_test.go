package reconcile_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/reconcile"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
)

// --- Phase C helpers: seed a running controller / a raw desired-marker -------

// seedController writes a controller.json for a live `ctl up` (an alive pid) and
// returns it, so a test can exercise the require-pid-1 gate and marker union
// without standing up a real daemon.
func seedController(t *testing.T, root store.Root, proc *fakeProc) reconcile.Controller {
	t.Helper()
	nonce, err := reconcile.NewNonce()
	if err != nil {
		t.Fatalf("mint nonce: %v", err)
	}
	ctrl := reconcile.Controller{PID: 99001, Nonce: nonce}
	if err := reconcile.WriteController(root, ctrl); err != nil {
		t.Fatalf("seed controller: %v", err)
	}
	proc.setAlive(ctrl.PID, true)
	return ctrl
}

// seedControllerPID writes a controller.json for the given pid without marking
// it alive — a stale record left by a crashed daemon, which must not satisfy the
// require-pid-1 gate.
func seedControllerPID(t *testing.T, root store.Root, pid int) {
	t.Helper()
	nonce, err := reconcile.NewNonce()
	if err != nil {
		t.Fatalf("mint nonce: %v", err)
	}
	if err := reconcile.WriteController(root, reconcile.Controller{PID: pid, Nonce: nonce}); err != nil {
		t.Fatalf("seed controller: %v", err)
	}
}

// writeRawMarker writes a desired-marker with an arbitrary nonce directly (the
// marker type is unexported), so a test can plant a stale-nonce marker.
func writeRawMarker(t *testing.T, root store.Root, ticket, att, nonce string) {
	t.Helper()
	body := []byte(`{"nonce":"` + nonce + `","stamp":"2026-08-05T00:00:00Z"}`)
	if err := os.WriteFile(root.DesiredMarkerPath(ticket, att), body, 0o644); err != nil {
		t.Fatalf("write raw marker: %v", err)
	}
}

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

// TestStartHandsOffToRunningDaemon: `ctl start` no longer drives a session
// itself — it requires a live `ctl up` and writes a transient desired-marker
// stamped with that controller's nonce. The daemon's next tick unions the marker
// into its desired set and admits the attempt through the same bringUp cascade +
// brief injection the enabled fleet uses. The attempt here is *disabled*, so the
// marker is the only thing that makes it desired — proving the union, not enable.
func TestStartHandsOffToRunningDaemon(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newDisabledTicket(t, ticket)

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	ctrl := seedController(t, w.root, proc)

	res, err := r.Start(ticket, att)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if res.ControllerPID != ctrl.PID {
		t.Fatalf("handed off to pid %d, want %d", res.ControllerPID, ctrl.PID)
	}

	// A disabled attempt is admitted only because the marker unions into desired.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	waitFor(t, "daemon admits the marked attempt", func() bool { return f.count() == 1 })
	live := f.at(0)
	waitFor(t, "brief on fresh spawn", func() bool {
		p := live.prompted()
		return len(p) > 0 && strings.Contains(p[0], "BRIEF")
	})

	// The full pipeline runs under the daemon: a promoted gotcha lands in the log.
	live.emit(bashCall("t1", `draiver log PROJ-1 --type gotcha "handed-off session works"`))
	waitFor(t, "promoted gotcha", func() bool {
		return hasType(logTypes(t, w.root, ticket, att), "gotcha")
	})
}

// TestStartRequiresController: with no running `ctl up`, start refuses rather
// than spawning an unsupervised orphan (require-pid-1). A controller record whose
// pid is dead does not count as live either.
func TestStartRequiresController(t *testing.T) {
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if _, err := r.Start(ticket, att); !errors.Is(err, reconcile.ErrNoController) {
		t.Fatalf("expected ErrNoController with no daemon, got %v", err)
	}

	// A stale record left by a crashed daemon (pid not alive) must not pass.
	seedControllerPID(t, w.root, 424242)
	if _, err := r.Start(ticket, att); !errors.Is(err, reconcile.ErrNoController) {
		t.Fatalf("dead controller pid should not satisfy require-pid-1, got %v", err)
	}
}

// TestDaemonSweepsStaleMarker: a desired-marker whose nonce does not match the
// live controller (e.g. left by a previous daemon boot) is ignored *and* swept
// off disk by the reconcile loop — so an imperative start never outlives the
// daemon it was handed to.
func TestDaemonSweepsStaleMarker(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newDisabledTicket(t, ticket)

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	// A live controller exists, but the marker carries a different boot's nonce.
	seedController(t, w.root, proc)
	writeRawMarker(t, w.root, ticket, att, "a-previous-boot")

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n := f.count(); n != 0 {
		t.Fatalf("a stale marker must not admit, but %d session(s) spawned", n)
	}
	if _, err := os.Stat(w.root.DesiredMarkerPath(ticket, att)); !os.IsNotExist(err) {
		t.Fatalf("stale marker should have been swept, stat err=%v", err)
	}
}

// TestRestartForegroundExitsWhenSessionEnds: restart still drives its session in
// the foreground (this phase), so a resumed session that exits on its own — its
// stream closes — makes Restart return without a cancel. This pins the
// runForeground exit-on-close path that `start` used to cover.
func TestRestartForegroundExitsWhenSessionEnds(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	// Admit once to record a session id + live pid, then detach the daemon ingest.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	orig, _ := sess.ReadIdentity()
	proc.setAlive(orig.PID, true)
	r.Close()

	done := make(chan error, 1)
	go func() { done <- r.Restart(ctx, ticket, att, reconcile.FlushNone, nil, nil) }()

	waitFor(t, "resume", func() bool { return f.count() == 2 })
	resumed := f.at(1)
	if err := resumed.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Restart returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Restart did not return after the session exited")
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
	go func() { done <- r.Restart(ctx, ticket, att, reconcile.FlushNone, nil, col.observe) }()

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

// TestRestartNewSessionSpawnsFreshOnSameWorktree is the L0 flush: restart
// --new-session discards the conversation, so the cascade must NOT resume the
// recorded id — it Spawns a fresh session on the *same* worktree and cold-starts
// it from the brief. Contrast TestRestartResumesWithoutRebrief, where a bare
// restart resumes the same id with no brief.
func TestRestartNewSessionSpawnsFreshOnSameWorktree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	// First bring-up records a session id, a live pid, and a worktree.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	orig, _ := sess.ReadIdentity()
	proc.setAlive(orig.PID, true)
	r.Close() // detach the daemon's ingest so it isn't racing the restart

	done := make(chan error, 1)
	go func() { done <- r.Restart(ctx, ticket, att, reconcile.FlushSession, nil, nil) }()

	// The flush cleared the id, so the second bring-up is a fresh Spawn, not a Resume.
	waitFor(t, "fresh spawn", func() bool { return f.count() == 2 })
	fresh := f.at(1)
	if fresh.resumedWith() != "" {
		t.Fatalf("--new-session must Spawn fresh, not Resume (got resume id %q)", fresh.resumedWith())
	}
	// It cold-starts from the brief (L0 was discarded)...
	waitFor(t, "brief on fresh spawn", func() bool {
		p := fresh.prompted()
		return len(p) > 0 && strings.Contains(p[0], "BRIEF")
	})
	// ...on the SAME worktree the resumed session would have used.
	waitFor(t, "same worktree", func() bool {
		id, err := sess.ReadIdentity()
		return err == nil && id.Worktree == orig.Worktree && id.SessionID != ""
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

// TestRestartNewWorktreeRebuildsWorktree is the L0+L1 flush: restart
// --new-worktree removes the checkout and its branch, then the cascade rebuilds a
// fresh worktree from HEAD and cold-starts a fresh session. The rebuilt checkout
// is a real git worktree on the per-attempt branch.
func TestRestartNewWorktreeRebuildsWorktree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	orig, _ := sess.ReadIdentity()
	proc.setAlive(orig.PID, true)

	// Drop an untracked file into the checkout — the uncommitted work --new-worktree
	// is meant to discard by rebuilding from HEAD.
	scratch := filepath.Join(orig.Worktree, "scratch.txt")
	if err := os.WriteFile(scratch, []byte("wip"), 0o644); err != nil {
		t.Fatalf("write scratch: %v", err)
	}
	r.Close()

	done := make(chan error, 1)
	go func() { done <- r.Restart(ctx, ticket, att, reconcile.FlushWorktree, nil, nil) }()

	// A fresh session is spawned and briefed on the rebuilt worktree.
	waitFor(t, "fresh spawn", func() bool { return f.count() == 2 })
	fresh := f.at(1)
	if fresh.resumedWith() != "" {
		t.Fatalf("--new-worktree must Spawn fresh, not Resume (got resume id %q)", fresh.resumedWith())
	}
	waitFor(t, "brief on fresh spawn", func() bool {
		p := fresh.prompted()
		return len(p) > 0 && strings.Contains(p[0], "BRIEF")
	})
	// The worktree was rebuilt from HEAD, so the uncommitted scratch file is gone.
	waitFor(t, "worktree rebuilt", func() bool {
		id, err := sess.ReadIdentity()
		if err != nil || id.Worktree == "" {
			return false
		}
		if _, statErr := os.Stat(filepath.Join(id.Worktree, "scratch.txt")); !os.IsNotExist(statErr) {
			return false
		}
		_, statErr := os.Stat(filepath.Join(id.Worktree, ".git"))
		return statErr == nil // a real (re)created worktree
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

// TestRestartNewAttemptForksAndStartsFresh is the L2 branch point: restart
// --new-attempt forks a new attempt (new id, `from` provenance), announces the
// fork before the stream starts, brings the new attempt up cold-started from the
// brief, and preserves the parent's log — recording the fork on it.
func TestRestartNewAttemptForksAndStartsFresh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket) // "0001"

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	orig, _ := sess.ReadIdentity()
	proc.setAlive(orig.PID, true)
	r.Close()

	announced := make(chan reconcile.RestartResult, 1)
	done := make(chan error, 1)
	go func() {
		done <- r.Restart(ctx, ticket, att, reconcile.FlushAttempt,
			func(res reconcile.RestartResult) { announced <- res }, nil)
	}()

	// The fork is announced up front with the new id and provenance.
	var res reconcile.RestartResult
	select {
	case res = <-announced:
	case <-time.After(3 * time.Second):
		t.Fatal("restart never announced the fork")
	}
	if !res.Forked || res.From != att || res.Attempt != "0002" {
		t.Fatalf("expected a fork into 0002 from %s, got %+v", att, res)
	}

	// The new attempt exists, records `from` provenance, and inherits the repo.
	child, err := attempt.LoadMeta(w.root, ticket, "0002")
	if err != nil {
		t.Fatalf("load forked attempt: %v", err)
	}
	if child.From != att {
		t.Fatalf("forked attempt should record from=%s, got %q", att, child.From)
	}

	// The forked attempt is brought up fresh (Spawn) and cold-started from the brief.
	waitFor(t, "fork spawned", func() bool { return f.count() == 2 })
	fresh := f.at(1)
	if fresh.resumedWith() != "" {
		t.Fatalf("a fork must Spawn fresh, not Resume (got resume id %q)", fresh.resumedWith())
	}
	waitFor(t, "brief on the fork", func() bool {
		p := fresh.prompted()
		return len(p) > 0 && strings.Contains(p[0], "BRIEF")
	})

	// The parent attempt is preserved: its session id survives and its log records
	// the fork as a note.
	parent, err := sess.ReadIdentity()
	if err != nil || parent.SessionID != orig.SessionID {
		t.Fatalf("parent session id must survive the fork: got %q want %q (err %v)", parent.SessionID, orig.SessionID, err)
	}
	if !hasType(logTypes(t, w.root, ticket, att), "note") {
		t.Fatal("parent log should record a fork note")
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

	// The attempt is enabled, so the daemon's admit drives bringUp: Resume the
	// recorded id, confirm-online, and fall through to a fresh Spawn on no-show.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}

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
	// The admitted fresh run is drained by t.Cleanup(r.Close); defer cancel() ends ctx.
}
