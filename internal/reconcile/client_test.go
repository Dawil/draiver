package reconcile_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/project"
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

// TestEnableNowRequiresController: `enable --now` is an imperative handoff like
// start/restart — it requires a live `ctl up` and checks the gate UP FRONT, so a
// no-daemon invocation persists NOTHING (the attempt stays disabled; the user can
// fall back to plain `enable`). A stale controller record (dead pid) does not
// count as live.
func TestEnableNowRequiresController(t *testing.T) {
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newDisabledTicket(t, ticket)

	f := &factory{}
	r := w.reconciler(t, f, newProc())
	t.Cleanup(r.Close)

	if _, err := r.EnableNow(ticket, att); !errors.Is(err, reconcile.ErrNoController) {
		t.Fatalf("expected ErrNoController with no daemon, got %v", err)
	}
	// The gate is up front: no enable event was persisted.
	if a, err := project.LoadAttempt(w.root, ticket, att); err != nil || a.Enabled {
		t.Fatalf("enable --now must not persist without a daemon: enabled=%v err=%v", a.Enabled, err)
	}

	seedControllerPID(t, w.root, 424242) // a crashed daemon's stale record
	if _, err := r.EnableNow(ticket, att); !errors.Is(err, reconcile.ErrNoController) {
		t.Fatalf("dead controller pid should not satisfy require-pid-1, got %v", err)
	}
}

// TestEnableNowEnablesDurablyAndBringsUp: with a live controller, enable --now
// sets the DURABLE enable bit (unlike start's transient marker) and the daemon
// brings the attempt up on that bit alone — no desired-marker is written, so a
// later disable can park it without a stale marker keeping it stuck desired
// (decision #29). This is the persistent counterpart of TestStartHandsOffToRunningDaemon.
func TestEnableNowEnablesDurablyAndBringsUp(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newDisabledTicket(t, ticket)

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	ctrl := seedController(t, w.root, proc)

	res, err := r.EnableNow(ticket, att)
	if err != nil {
		t.Fatalf("EnableNow: %v", err)
	}
	if res.ControllerPID != ctrl.PID {
		t.Fatalf("handed off to pid %d, want %d", res.ControllerPID, ctrl.PID)
	}

	// Durable: the enable bit is set (it survives a daemon restart; a marker would not).
	if a, err := project.LoadAttempt(w.root, ticket, att); err != nil || !a.Enabled {
		t.Fatalf("enable --now must set the durable enable bit: enabled=%v err=%v", a.Enabled, err)
	}
	// And NO transient marker was written — the enable bit alone makes it desired.
	if _, err := os.Stat(w.root.DesiredMarkerPath(ticket, att)); !os.IsNotExist(err) {
		t.Fatalf("enable --now must not write a desired-marker, stat err=%v", err)
	}

	// The daemon admits it on the enable bit and cold-starts it from the brief.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	waitFor(t, "daemon admits the enabled attempt", func() bool { return f.count() == 1 })
	live := f.at(0)
	waitFor(t, "brief on fresh spawn", func() bool {
		p := live.prompted()
		return len(p) > 0 && strings.Contains(p[0], "BRIEF")
	})
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

// TestRestartRequiresController: like start, restart is a daemon handoff and
// refuses without a running `ctl up` — and it checks the gate UP FRONT, before
// reaping anything, so it never tears a session down it cannot hand back. A stale
// controller record (dead pid) does not count as live.
func TestRestartRequiresController(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	// Bring a session up so there is a live pid a buggy restart could reap.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	orig, _ := sess.ReadIdentity()
	proc.setAlive(orig.PID, true)

	if _, err := r.Restart(ctx, ticket, att, reconcile.FlushNone); !errors.Is(err, reconcile.ErrNoController) {
		t.Fatalf("expected ErrNoController with no daemon, got %v", err)
	}
	// The gate is up front: the running session was NOT reaped.
	after, _ := sess.ReadIdentity()
	if after.PID != orig.PID {
		t.Fatalf("restart must not reap before the require-pid-1 gate: pid %d -> %d", orig.PID, after.PID)
	}

	seedControllerPID(t, w.root, 424242) // a crashed daemon's stale record
	if _, err := r.Restart(ctx, ticket, att, reconcile.FlushNone); !errors.Is(err, reconcile.ErrNoController) {
		t.Fatalf("dead controller pid should not satisfy require-pid-1, got %v", err)
	}
}

// TestStopLeavesImperativeFleet: stop is the symmetric counterpart of start —
// it reaps the process AND removes the desired-marker, so a stopped attempt
// leaves the imperative fleet and the daemon does not re-admit it. Here a
// disabled attempt is desired only via a marker; after stop the marker is gone
// and a tick admits nothing.
func TestStopLeavesImperativeFleet(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newDisabledTicket(t, ticket)

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	seedController(t, w.root, proc)

	// start hands off: writes the marker for the disabled attempt.
	if _, err := r.Start(ticket, att); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := os.Stat(w.root.DesiredMarkerPath(ticket, att)); err != nil {
		t.Fatalf("start should have written a marker: %v", err)
	}

	// stop reaps and clears the marker (no session yet — reap is a no-op here).
	if _, err := r.Stop(ticket, att); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Stat(w.root.DesiredMarkerPath(ticket, att)); !os.IsNotExist(err) {
		t.Fatalf("stop must remove the desired-marker, stat err=%v", err)
	}

	// With the marker gone the disabled attempt is no longer desired: nothing admits.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n := f.count(); n != 0 {
		t.Fatalf("a stopped (unmarked, disabled) attempt must not admit, but %d spawned", n)
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
// teardown depth selected, Restart reaps the process (clearing the pid, keeping
// the session id) and hands the attempt back to the daemon, whose next tick
// re-admits the spent run (readmittable: spent + pid==0, still desired) and the
// start cascade Resumes the SAME session id — a continue, not a new attempt. And
// because the session layer (L0) was kept, it does NOT re-inject the full
// cold-start brief. It IS still driven, though: a headless stream-json process
// needs a user turn to continue, so the resume gets the short resume nudge (not
// the whole brief re-dumped) — the brief is coupled to a reset, the nudge to a
// resume (drvctl-016 + drvctl-022).
func TestRestartResumesWithoutRebrief(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket) // enabled: desired via the enable bit, no marker

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	ctrl := seedController(t, w.root, proc)

	// First bring-up records a session id and a live pid; the run is live in the table.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	waitFor(t, "first spawn", func() bool { return f.count() == 1 })
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	orig, _ := sess.ReadIdentity()
	proc.setAlive(orig.PID, true)

	res, err := r.Restart(ctx, ticket, att, reconcile.FlushNone)
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if res.ControllerPID != ctrl.PID || res.Forked {
		t.Fatalf("unexpected restart result: %+v", res)
	}
	// The reap cleared the pid but kept the session id; an enabled attempt gets no marker.
	after, _ := sess.ReadIdentity()
	if after.PID != 0 {
		t.Fatalf("restart should clear the pid, got %d", after.PID)
	}
	if after.SessionID != orig.SessionID {
		t.Fatalf("restart (FlushNone) must keep the session id: %q -> %q", orig.SessionID, after.SessionID)
	}
	if _, statErr := os.Stat(w.root.DesiredMarkerPath(ticket, att)); !os.IsNotExist(statErr) {
		t.Fatalf("an enabled attempt must not be marked (would outlive a disable), stat err=%v", statErr)
	}

	// The reaped process exits — its stream closes, so the run becomes spent.
	if err := f.at(0).Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// The daemon ticks on an interval; once it observes the spent run it re-admits
	// it and Resumes the same id. Poll a tick (the run becomes spent asynchronously).
	waitFor(t, "resume", func() bool {
		if err := r.Tick(ctx); err != nil {
			t.Fatalf("re-admit tick: %v", err)
		}
		return f.count() == 2
	})
	resumed := f.at(1)
	waitFor(t, "resume on the surviving id", func() bool { return resumed.resumedWith() == orig.SessionID })
	// Driven, but with the nudge — not the full brief re-dumped.
	waitFor(t, "resume nudge", func() bool { return len(resumed.prompted()) > 0 })
	if p := resumed.prompted()[0]; strings.Contains(p, "BRIEF") {
		t.Fatalf("a resume must be nudged, not re-briefed, but got the brief: %q", p)
	}
}

// TestRestartNewSessionSpawnsFreshOnSameWorktree is the L0 flush: restart
// --new-session discards the conversation, so the cascade must NOT resume the
// recorded id — it Spawns a fresh session on the *same* worktree and cold-starts
// it from the brief. Contrast TestRestartResumesWithoutRebrief, where a bare
// restart resumes the same id with no brief.
func TestRestartNewSessionSpawnsFreshOnSameWorktree(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	seedController(t, w.root, proc)

	// First bring-up records a session id, a live pid, and a worktree.
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	waitFor(t, "first spawn", func() bool { return f.count() == 1 })
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	orig, _ := sess.ReadIdentity()
	proc.setAlive(orig.PID, true)

	if _, err := r.Restart(ctx, ticket, att, reconcile.FlushSession); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	// The flush discarded L0: the session id is cleared, the worktree is kept.
	flushed, _ := sess.ReadIdentity()
	if flushed.SessionID != "" {
		t.Fatalf("--new-session must clear the session id, got %q", flushed.SessionID)
	}
	if flushed.Worktree != orig.Worktree {
		t.Fatalf("--new-session must keep the worktree: %q -> %q", orig.Worktree, flushed.Worktree)
	}

	// The reaped process exits; the daemon re-admits and, with no id, Spawns fresh.
	if err := f.at(0).Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waitFor(t, "fresh spawn", func() bool {
		if err := r.Tick(ctx); err != nil {
			t.Fatalf("re-admit tick: %v", err)
		}
		return f.count() == 2
	})
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
}

// TestRestartNewWorktreeRebuildsWorktree is the L0+L1 flush: restart
// --new-worktree removes the checkout and its branch, then the cascade rebuilds a
// fresh worktree from HEAD and cold-starts a fresh session. The rebuilt checkout
// is a real git worktree on the per-attempt branch.
func TestRestartNewWorktreeRebuildsWorktree(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket)

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	seedController(t, w.root, proc)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	waitFor(t, "first spawn", func() bool { return f.count() == 1 })
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

	if _, err := r.Restart(ctx, ticket, att, reconcile.FlushWorktree); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	// The reaped process exits; the daemon re-admits, rebuilds the worktree, and Spawns.
	if err := f.at(0).Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waitFor(t, "fresh spawn", func() bool {
		if err := r.Tick(ctx); err != nil {
			t.Fatalf("re-admit tick: %v", err)
		}
		return f.count() == 2
	})
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
}

// TestRestartNewAttemptForksParksParentHandsOffChild is the L2 branch point:
// restart --new-attempt forks a new attempt (new id, `from` provenance), PARKS
// the parent (a disable event + a swept marker, so it leaves the supervised
// fleet — decision #23), and hands the *child* off to the daemon, which brings it
// up fresh from the brief. The parent's log and session id are preserved; the
// daemon runs the fork, not both.
func TestRestartNewAttemptForksParksParentHandsOffChild(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket) // "0001", enabled

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	ctrl := seedController(t, w.root, proc)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	waitFor(t, "first spawn", func() bool { return f.count() == 1 })
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	orig, _ := sess.ReadIdentity()
	proc.setAlive(orig.PID, true)

	res, err := r.Restart(ctx, ticket, att, reconcile.FlushAttempt)
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if !res.Forked || res.From != att || res.Attempt != "0002" || res.ControllerPID != ctrl.PID {
		t.Fatalf("expected a fork into 0002 from %s handed to pid %d, got %+v", att, ctrl.PID, res)
	}

	// The new attempt exists and records `from` provenance.
	child, err := attempt.LoadMeta(w.root, ticket, "0002")
	if err != nil {
		t.Fatalf("load forked attempt: %v", err)
	}
	if child.From != att {
		t.Fatalf("forked attempt should record from=%s, got %q", att, child.From)
	}

	// The parent is parked: its log records both the fork note and a disable event,
	// its session id survives, and it carries no desired-marker.
	types := logTypes(t, w.root, ticket, att)
	if !hasType(types, "note") {
		t.Fatal("parent log should record a fork note")
	}
	if !hasType(types, "disable") {
		t.Fatal("parent should be parked with a disable event (decision #23)")
	}
	parent, err := sess.ReadIdentity()
	if err != nil || parent.SessionID != orig.SessionID {
		t.Fatalf("parent session id must survive the fork: got %q want %q (err %v)", parent.SessionID, orig.SessionID, err)
	}
	if _, statErr := os.Stat(w.root.DesiredMarkerPath(ticket, att)); !os.IsNotExist(statErr) {
		t.Fatalf("parked parent must carry no marker, stat err=%v", statErr)
	}
	// The child carries the transient handoff marker.
	if _, statErr := os.Stat(w.root.DesiredMarkerPath(ticket, "0002")); statErr != nil {
		t.Fatalf("child should carry a desired-marker: %v", statErr)
	}

	// The parent process exits; the daemon's next tick runs the fork (fresh spawn +
	// brief) and does NOT resume the parked parent.
	if err := f.at(0).Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("re-admit tick: %v", err)
	}
	waitFor(t, "fork spawned", func() bool { return f.count() == 2 })
	fresh := f.at(1)
	if fresh.resumedWith() != "" {
		t.Fatalf("a fork must Spawn fresh, not Resume (got resume id %q)", fresh.resumedWith())
	}
	waitFor(t, "brief on the fork", func() bool {
		p := fresh.prompted()
		return len(p) > 0 && strings.Contains(p[0], "BRIEF")
	})
}

// TestStartNewAttemptForksAndLeavesParent is the parallel-branch counterpart of
// restart --new-attempt: `start --new-attempt` forks a child (new id, provenance)
// and hands it off, but leaves the parent exactly as it was — still enabled, still
// running, not parked. Both paths run (decision #23).
func TestStartNewAttemptForksAndLeavesParent(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket) // enabled parent

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	ctrl := seedController(t, w.root, proc)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	waitFor(t, "parent spawn", func() bool { return f.count() == 1 })
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	orig, _ := sess.ReadIdentity()
	proc.setAlive(orig.PID, true)

	res, err := r.StartNewAttempt(ticket, att)
	if err != nil {
		t.Fatalf("StartNewAttempt: %v", err)
	}
	if !res.Forked || res.From != att || res.Attempt != "0002" || res.ControllerPID != ctrl.PID {
		t.Fatalf("expected a fork into 0002 from %s handed to pid %d, got %+v", att, ctrl.PID, res)
	}

	child, err := attempt.LoadMeta(w.root, ticket, "0002")
	if err != nil {
		t.Fatalf("load forked attempt: %v", err)
	}
	if child.From != att {
		t.Fatalf("forked attempt should record from=%s, got %q", att, child.From)
	}

	// The parent is left as it was: a fork note but NO disable (not parked), its
	// session id and running pid intact, and no marker (it stays desired via enable).
	types := logTypes(t, w.root, ticket, att)
	if !hasType(types, "note") {
		t.Fatal("parent log should record a start-fork note")
	}
	if hasType(types, "disable") {
		t.Fatal("start --new-attempt must NOT park the parent (decision #23)")
	}
	parent, _ := sess.ReadIdentity()
	if parent.SessionID != orig.SessionID || parent.PID != orig.PID {
		t.Fatalf("parent must be left running as-is: got id=%q pid=%d want id=%q pid=%d", parent.SessionID, parent.PID, orig.SessionID, orig.PID)
	}

	// The child is admitted fresh alongside the still-live parent (parent's live run
	// is left strictly alone; only the child is brought up).
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("re-admit tick: %v", err)
	}
	waitFor(t, "child spawn", func() bool { return f.count() == 2 })
	if f.at(1).resumedWith() != "" {
		t.Fatalf("a fork must Spawn fresh, not Resume (got resume id %q)", f.at(1).resumedWith())
	}
	if f.at(0).wasKilled() {
		t.Fatal("parent's session must be left running, not reaped")
	}
}

// TestStopOnEnabledIsTransient pins decision #29's consequence: a bare `ctl stop`
// on a still-enabled attempt is transient — reap removes no durable desire (the
// enable bit stays), so the daemon's next tick re-admits the spent run and Resumes
// it. To durably stop an enabled attempt you `disable`, not `stop`.
func TestStopOnEnabledIsTransient(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ticket := "PROJ-1"
	att := w.newTicket(t, ticket) // enabled

	f := &factory{}
	proc := newProc()
	r := w.reconciler(t, f, proc)
	t.Cleanup(r.Close)

	seedController(t, w.root, proc)

	if err := r.Tick(ctx); err != nil {
		t.Fatalf("admit tick: %v", err)
	}
	waitFor(t, "first spawn", func() bool { return f.count() == 1 })
	sess, _ := session.Open(w.root, ticket, att)
	t.Cleanup(func() { sess.Close() })
	orig, _ := sess.ReadIdentity()
	proc.setAlive(orig.PID, true)

	if _, err := r.Stop(ticket, att); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := f.at(0).Kill(); err != nil { // the reaped process exits
		t.Fatalf("kill: %v", err)
	}

	waitFor(t, "resume after transient stop", func() bool {
		if err := r.Tick(ctx); err != nil {
			t.Fatalf("re-admit tick: %v", err)
		}
		return f.count() == 2
	})
	if f.at(1).resumedWith() != orig.SessionID {
		t.Fatalf("a stop on an enabled attempt should be transient (resume the same id), got resume id %q", f.at(1).resumedWith())
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
