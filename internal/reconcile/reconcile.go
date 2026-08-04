// Package reconcile is draiverctld's reconcile loop — the supervisor's active
// half, the piece that ties drvctl-001–007 together into a running daemon. It is
// declarative, not imperative: each tick reads the *desired* set (which attempts
// should have a live, progressing session) and the *actual* set (what this daemon
// is running, plus live processes it re-adopted on restart), and closes the gap.
//
// The tick has four moves, mirroring docs/draiverctl.md "the reconcile loop":
//
//   - Admit  — a desired attempt with no live session gets one: manage.Handle
//     Spawns a fresh session (or Resumes the surviving cattle handle when a
//     session.json is already on record), a watch/gate/protocol ingest goroutine
//     is attached to its stream, and the cold-start brief is injected.
//   - Watch  — the ingest goroutine tees every stream line to the journal, meters
//     tokens/cost/context, and promotes semantic events into the durable log
//     (internal/watch).
//   - Gate   — the same goroutine runs the two enforcement seams on each
//     tool-permission callback: the protocol gate (withhold an edit until its
//     rationale is logged, internal/protocol) then the permission gate (allow, or
//     escalate-and-halt, internal/gate).
//   - Retire — an attempt that left the desired set is stopped. A terminal one
//     (Review/Done) has its worktree cleaned; a blocked one (Needs-me) is parked
//     with its worktree kept warm, so a later `resolve` — which flips it back to
//     Running — re-admits it via Resume with no work lost.
//
// Respawn policy, the progress watchdog, StartLimit ceilings, budgets, the
// dependency DAG, and instant path-activation on `resolution` are all Tier 1+ and
// deliberately out of scope here (see the doc's implementation order). This is
// Tier 0: one adapter, admit-on-enabled, no scheduler.
//
// The daemon holds no authoritative state. The append-only log is the truth; the
// only runtime state — the session/ directory and this in-memory run table — is
// rebuildable. A draiverctld restart calls Adopt to rebuild its view from disk
// plus a scan of the process table (see Adopt), which is why draiverctld is itself
// cattle and survives a restart without disturbing the sessions still running.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/gate"
	"github.com/Dawil/draiver/internal/manage"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/protocol"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/watch"
	"github.com/Dawil/draiver/internal/worktree"
)

// defaultAdapter is the adapter name assumed for an attempt whose attempt.md
// records none — Claude Code is the first and reference adapter (Tier 0).
const defaultAdapter = "claude-code"

// Proc is the seam the reconciler uses to reason about foreign session processes
// on restart: whether a recorded pid is still alive (so it is re-adopted rather
// than duplicated) and how to stop one it no longer wants. It is an interface so a
// test can drive re-adoption deterministically without real pids; OSProc is the
// production implementation.
type Proc interface {
	// Alive reports whether the process with pid is still running. A best-effort
	// signal-0 probe in production; pid reuse is an accepted Tier-0 risk.
	Alive(pid int) bool
	// Terminate asks the process with pid to stop (SIGTERM in production). Used
	// only to retire an adopted, un-streamed session; best-effort.
	Terminate(pid int) error
}

// AdapterFor returns a factory that mints a fresh agent.Adapter for each
// Spawn/Resume of the named adapter (attempt.md's Tool). One adapter value is
// single-use, so a factory — not an adapter — is returned. It errors for an
// unknown adapter name.
type AdapterFor func(name string) (func() agent.Adapter, error)

// Options configures a Reconciler. Root, Worktrees, Adapters, and Actor are
// required; the rest have sensible defaults.
type Options struct {
	// Root is the ticket data root — the source of truth the desired set is
	// derived from and the durable log is appended to.
	Root store.Root

	// Worktrees isolates each session in its own git checkout. One Manager over
	// the repo the agents work in.
	Worktrees *worktree.Manager

	// Adapters resolves an attempt's adapter name to an adapter factory. Required;
	// Tier 0 ships a single "claude-code" entry.
	Adapters AdapterFor

	// Actor is the identity stamped on promoted log events and escalations, e.g.
	// "agent:claude-code". Required.
	Actor string

	// PermPolicy governs the permission gate (allow vs escalate per tool). Zero
	// value defaults to gate.ReadOnly().
	PermPolicy gate.Policy

	// ProtoPolicy governs the protocol gate (which edit tools are withheld until
	// justified). Zero value defaults to protocol.Edits().
	ProtoPolicy protocol.Policy

	// BaseSpec is the session spec template each session is brought up with (model
	// is taken from the attempt when unset, and WorkDir is always the worktree).
	// The daemon typically sets PermissionMode here so the agent routes tool use
	// through the callback the gates answer.
	BaseSpec agent.SessionSpec

	// Proc probes and stops foreign session processes on restart. Zero value
	// defaults to OSProc{}.
	Proc Proc

	// Now sources the current time; defaults to time.Now.
	Now func() time.Time

	// Logf receives non-fatal operational messages (an admit that failed, a gate
	// error). Defaults to a no-op — a tick never fails the daemon over one
	// attempt.
	Logf func(format string, args ...any)
}

// Reconciler is the running daemon's control loop. Construct it with New, then
// drive it with Run (Adopt once, Tick on an interval) or call Adopt/Tick directly
// in a test. It is safe for concurrent use; Tick is expected to run from a single
// loop goroutine while ingest goroutines mutate meters and the log underneath it.
type Reconciler struct {
	opt Options

	mu   sync.Mutex
	runs map[worktree.Key]*run
}

// run is one entry in the actual-state table. A run this daemon owns carries the
// live manage.Handle and the ingest goroutine driving it. An adopted run — a
// foreign live process recognized on restart — carries only its pid: Tier 0 does
// not re-stream a process it did not spawn (that is the Runtime.Attach seam), it
// only recognizes it so the tick will not spawn a duplicate.
type run struct {
	handle *manage.Handle
	sess   *session.Store
	cancel context.CancelFunc
	done   chan struct{}

	adopted bool
	pid     int
}

// New validates opt, applies defaults, and returns a Reconciler with an empty run
// table.
func New(opt Options) (*Reconciler, error) {
	switch {
	case opt.Root.Dir == "":
		return nil, errors.New("reconcile: data root is required")
	case opt.Worktrees == nil:
		return nil, errors.New("reconcile: worktree manager is required")
	case opt.Adapters == nil:
		return nil, errors.New("reconcile: adapter resolver is required")
	case opt.Actor == "":
		return nil, errors.New("reconcile: actor is required")
	}
	if opt.PermPolicy.Default == "" && opt.PermPolicy.Tools == nil {
		opt.PermPolicy = gate.ReadOnly()
	}
	if opt.ProtoPolicy.Default == "" && opt.ProtoPolicy.Tools == nil {
		opt.ProtoPolicy = protocol.Edits()
	}
	if opt.Proc == nil {
		opt.Proc = OSProc{}
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	return &Reconciler{opt: opt, runs: map[worktree.Key]*run{}}, nil
}

// Run reconciles the fleet until ctx is cancelled: Adopt once to rebuild state
// from disk, an immediate first Tick, then a Tick every interval. Cancellation
// drains — the ingest goroutines stop but the sessions are left running, so a
// later daemon start re-adopts them. It returns nil on a clean (cancelled)
// shutdown, or the error from Adopt (the one failure fatal to starting up).
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) error {
	if err := r.Adopt(ctx); err != nil {
		return fmt.Errorf("reconcile: adopt on start: %w", err)
	}
	if err := r.Tick(ctx); err != nil {
		r.opt.Logf("reconcile: initial tick: %v", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.drain()
			return nil
		case <-t.C:
			if err := r.Tick(ctx); err != nil {
				r.opt.Logf("reconcile: tick: %v", err)
			}
		}
	}
}

// Tick runs one reconcile pass: admit desired attempts with no live session and
// retire live sessions no longer desired. A per-attempt failure is logged and the
// pass continues; Tick only returns an error if the desired set itself cannot be
// read.
func (r *Reconciler) Tick(ctx context.Context) error {
	desired, err := r.desired()
	if err != nil {
		return fmt.Errorf("reconcile: read desired set: %w", err)
	}

	r.mu.Lock()
	current := make([]worktree.Key, 0, len(r.runs))
	for k := range r.runs {
		current = append(current, k)
	}
	r.mu.Unlock()
	currentSet := make(map[worktree.Key]bool, len(current))
	for _, k := range current {
		currentSet[k] = true
	}

	// Admit: desired but not running.
	for key, a := range desired {
		if currentSet[key] {
			continue
		}
		if err := r.admit(ctx, a); err != nil {
			r.opt.Logf("reconcile: admit %s/%s: %v", key.Ticket, key.Attempt, err)
		}
	}

	// Retire: running but no longer desired. The attempt's current state decides
	// whether the worktree is cleaned (terminal) or kept warm (blocked).
	for _, key := range current {
		if _, ok := desired[key]; ok {
			continue
		}
		st := r.stateOf(key)
		r.retire(ctx, key, st != project.NeedsMe)
	}
	return nil
}

// desired returns the attempts that should have a live session — those whose
// derived control state is Running. Needs-me (blocked on a human), Review (a claim
// awaiting ratification), and Done (closed) are all not desired. With no explicit
// enable flag yet (Tier 2), every Running attempt is enabled.
func (r *Reconciler) desired() (map[worktree.Key]project.Attempt, error) {
	all, err := project.LoadAll(r.opt.Root)
	if err != nil {
		return nil, err
	}
	out := make(map[worktree.Key]project.Attempt)
	for _, a := range all {
		if a.State == project.Running {
			out[worktree.Key{Ticket: a.Ticket, Attempt: a.ID}] = a
		}
	}
	return out, nil
}

// stateOf reads an attempt's current control state, treating a vanished or
// unreadable attempt as Done (terminal) so a retire cleans up rather than parking.
func (r *Reconciler) stateOf(key worktree.Key) project.State {
	a, err := project.LoadAttempt(r.opt.Root, key.Ticket, key.Attempt)
	if err != nil {
		return project.Done
	}
	return a.State
}

// wired is a brought-up live session with the ingest machinery bound to its
// stream: the handle owning the process, the session store, and the two gates
// plus the watcher each of its events is dispatched through. Both admit (the
// daemon, streaming in a background goroutine) and Start (the client, streaming
// in the foreground) obtain one from bringUp — the single place the session and
// its watch/gate/protocol pipeline are wired, so the two entry points cannot
// drift apart.
type wired struct {
	handle    *manage.Handle
	sess      *session.Store
	protoGate *protocol.Gate
	permGate  *gate.Gate
	watcher   *watch.Watcher
}

// bringUp opens an attempt's session store, binds a manage.Handle, Spawns a
// fresh session (or Resumes the recorded cattle handle when one is on file),
// snapshots the protocol-gate baseline, and constructs the permission gate and
// watcher — everything needed to consume the live stream, but not yet consuming
// it. On any failure it unwinds cleanly (reap + close) so a failed bring-up
// leaves nothing half-live. The caller injects the cold-start brief once it is
// ready to consume the stream.
func (r *Reconciler) bringUp(ctx context.Context, a project.Attempt) (*wired, error) {
	adapterName := a.Tool
	if adapterName == "" {
		adapterName = defaultAdapter
	}
	newAdapter, err := r.opt.Adapters(adapterName)
	if err != nil {
		return nil, fmt.Errorf("resolve adapter %q: %w", adapterName, err)
	}

	sess, err := session.Open(r.opt.Root, a.Ticket, a.ID)
	if err != nil {
		return nil, err
	}

	handle, err := manage.New(sess, r.opt.Worktrees, newAdapter, manage.Config{
		Ticket:  a.Ticket,
		Attempt: a.ID,
		Adapter: adapterName,
		Model:   a.Model,
		Spec:    r.opt.BaseSpec,
	})
	if err != nil {
		_ = sess.Close()
		return nil, err
	}

	// A recorded session id means this attempt already ran: reuse the cattle
	// handle and Resume rather than Spawn a second, unrelated session.
	resuming := false
	if id, err := sess.ReadIdentity(); err == nil && id.SessionID != "" {
		resuming = true
	}
	if resuming {
		err = handle.Resume(ctx)
	} else {
		err = handle.Spawn(ctx)
	}
	if err != nil {
		_ = sess.Close()
		return nil, err
	}

	// The protocol gate's baseline is the log tail *at session start*, so a
	// justifying decision/gotcha must be logged during this session. Snapshot it
	// now, right after the session came up and before it does any work.
	protoGate, err := protocol.New(r.opt.Root, a.Ticket, a.ID, r.opt.ProtoPolicy, handle)
	if err != nil {
		_ = handle.Kill()
		_ = sess.Close()
		return nil, fmt.Errorf("protocol gate: %w", err)
	}
	permGate := gate.New(r.opt.Root, a.Ticket, a.ID, r.opt.Actor, r.opt.PermPolicy, handle, handle.Kill)
	watcher := watch.New(r.opt.Root, a.Ticket, a.ID, r.opt.Actor, sess, watch.ProtocolRecognizer{Ticket: a.Ticket})

	return &wired{handle: handle, sess: sess, protoGate: protoGate, permGate: permGate, watcher: watcher}, nil
}

// admit brings up a session for a desired attempt and attaches the ingest
// goroutine to its live stream, then injects the cold-start brief — the daemon's
// background path. bringUp does the wiring (and its clean unwind on failure); a
// successful admit records the run so the next tick sees it as running.
func (r *Reconciler) admit(ctx context.Context, a project.Attempt) error {
	key := worktree.Key{Ticket: a.Ticket, Attempt: a.ID}

	w, err := r.bringUp(ctx, a)
	if err != nil {
		return err
	}

	stream := w.handle.Stream()
	ictx, cancel := context.WithCancel(ctx)
	rn := &run{handle: w.handle, sess: w.sess, cancel: cancel, done: make(chan struct{})}

	r.mu.Lock()
	r.runs[key] = rn
	r.mu.Unlock()

	go r.ingest(ictx, key, rn, w.protoGate, w.permGate, w.watcher, stream)

	// Inject the cold-start brief last, so the stream is already being consumed
	// when the agent starts producing. A brief that fails to build/deliver does
	// not tear the live session down — it is logged and the session works on.
	if err := protocol.InjectBrief(ctx, r.opt.Root, a.Ticket, a.ID, w.handle); err != nil {
		r.opt.Logf("reconcile: inject brief %s/%s: %v", a.Ticket, a.ID, err)
	}
	return nil
}

// ingest is the Watch+Gate goroutine for one live session: it ranges the session
// stream until it closes (the session exited) or ctx is cancelled (retire/drain),
// dispatching each event. It never removes its own run from the table — only
// retire/drain do — so a session that exits on its own leaves a spent entry that
// keeps the next tick from re-admitting it (Tier 0 has no respawn ceiling, so
// re-admitting a crashed session would be an unbounded loop).
func (r *Reconciler) ingest(ctx context.Context, key worktree.Key, rn *run, pg *protocol.Gate, permGate *gate.Gate, w *watch.Watcher, stream <-chan agent.Event) {
	defer close(rn.done)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-stream:
			if !ok {
				return
			}
			r.dispatch(ctx, key, pg, permGate, w, ev)
		}
	}
}

// dispatch handles one event: first the journal/meter/promote pass (watch owns
// every kind; a permission event is merely teed), then, only for a permission
// event, the two gates in order. The protocol gate is a veto that either withholds
// (denies, session keeps running so the agent can log and retry) or clears without
// answering; a cleared request falls through to the permission gate, which allows
// it or escalates-and-halts. Ordering protocol → permission is what keeps the two
// from double-answering the agent.
func (r *Reconciler) dispatch(ctx context.Context, key worktree.Key, pg *protocol.Gate, permGate *gate.Gate, w *watch.Watcher, ev agent.Event) {
	if _, err := w.Process(ev); err != nil {
		r.opt.Logf("reconcile: watch %s/%s: %v", key.Ticket, key.Attempt, err)
	}

	if ev.Kind != agent.EventPermission {
		return
	}
	out, err := pg.Consider(ctx, ev)
	if err != nil {
		r.opt.Logf("reconcile: protocol gate %s/%s: %v", key.Ticket, key.Attempt, err)
		return
	}
	if out == protocol.Withheld {
		return
	}
	if _, err := permGate.Consider(ctx, ev); err != nil {
		r.opt.Logf("reconcile: permission gate %s/%s: %v", key.Ticket, key.Attempt, err)
	}
}

// retire stops the session for key and removes it from the table. removeWorktree
// distinguishes a terminal retire (Review/Done — clean the checkout, keep the
// branch as a crash-recovery net) from parking a blocked attempt (Needs-me — keep
// the worktree warm so a post-resolution Resume continues where it left off). An
// adopted (foreign) session is stopped by pid, the only handle Tier 0 has on it.
func (r *Reconciler) retire(ctx context.Context, key worktree.Key, removeWorktree bool) {
	r.mu.Lock()
	rn := r.runs[key]
	delete(r.runs, key)
	r.mu.Unlock()
	if rn == nil {
		return
	}

	if rn.adopted {
		if rn.pid != 0 && r.opt.Proc.Alive(rn.pid) {
			if err := r.opt.Proc.Terminate(rn.pid); err != nil {
				r.opt.Logf("reconcile: terminate adopted %s/%s (pid %d): %v", key.Ticket, key.Attempt, rn.pid, err)
			}
		}
	} else {
		rn.cancel()
		if err := rn.handle.Kill(); err != nil {
			r.opt.Logf("reconcile: kill %s/%s: %v", key.Ticket, key.Attempt, err)
		}
		<-rn.done
		_ = rn.sess.Close()
	}

	if removeWorktree {
		if err := r.opt.Worktrees.Remove(ctx, key, worktree.RemoveOptions{Force: true}); err != nil {
			r.opt.Logf("reconcile: remove worktree %s/%s: %v", key.Ticket, key.Attempt, err)
		}
	}
}

// Adopt rebuilds the daemon's view from disk on start, so a restarted draiverctld
// converges without disturbing what is still running. It is the crash/restart
// recovery entry point Run calls before its first Tick:
//
//  1. Scan every attempt with a session.json — the ones that have (or had) a
//     session — and keep their worktrees.
//  2. worktree.Reconcile against that keep-set: sweep checkouts left by a crash or
//     by a session nothing wants back, and re-attach the survivors.
//  3. Re-adopt every recorded session whose pid is still alive into the run table,
//     so the next Tick recognizes it as running and does not spawn a duplicate
//     agent into the same worktree. A session whose pid is dead is left out: if
//     its attempt is still Running the next Tick re-admits it via Resume (regaining
//     a stream); if not, it stays parked.
//
// Adopting a live pid without re-streaming it is deliberate: the agent.Adapter
// seam has no attach verb (re-attaching a fresh stream to a foreign process is the
// Runtime.Attach seam, Tier 1+), and a ctld restart must not reap healthy agents.
// The cost is that an adopted session is not metered/promoted until it exits and
// is resumed — an accepted Tier-0 gap.
func (r *Reconciler) Adopt(ctx context.Context) error {
	tickets, err := r.opt.Root.ListTickets()
	if err != nil {
		return err
	}

	var keep []worktree.Key
	live := map[worktree.Key]int{}
	for _, ticket := range tickets {
		atts, err := r.opt.Root.ListAttempts(ticket)
		if err != nil {
			return err
		}
		for _, att := range atts {
			key := worktree.Key{Ticket: ticket, Attempt: att}
			id, ok := r.readIdentity(key)
			if !ok {
				continue // no session.json — nothing to re-adopt or keep
			}
			keep = append(keep, key)
			if id.PID != 0 && r.opt.Proc.Alive(id.PID) {
				live[key] = id.PID
			}
		}
	}

	if _, err := r.opt.Worktrees.Reconcile(ctx, keep); err != nil {
		return fmt.Errorf("reconcile worktrees on adopt: %w", err)
	}

	r.mu.Lock()
	for key, pid := range live {
		if _, exists := r.runs[key]; exists {
			continue
		}
		r.runs[key] = &run{adopted: true, pid: pid}
	}
	r.mu.Unlock()
	return nil
}

// readIdentity loads an attempt's persisted session identity, reporting ok=false
// when it has no session/ yet or the record cannot be read.
func (r *Reconciler) readIdentity(key worktree.Key) (session.Identity, bool) {
	sess, err := session.Open(r.opt.Root, key.Ticket, key.Attempt)
	if err != nil {
		return session.Identity{}, false
	}
	defer sess.Close()
	id, err := sess.ReadIdentity()
	if err != nil {
		return session.Identity{}, false
	}
	return id, true
}

// Close drains the reconciler: it stops the ingest goroutines and detaches from
// the sessions without reaping them (see drain), the `draiverctl down` semantics.
// It is safe to call after Run has returned; a second call is a no-op.
func (r *Reconciler) Close() { r.drain() }

// drain stops the ingest goroutines on shutdown without reaping the agents: the
// sessions keep running and their session.json keeps a live pid, so a later
// daemon start re-adopts them (Adopt). This is the "ctld is cattle too" property —
// the daemon can exit and come back without interrupting the fleet.
func (r *Reconciler) drain() {
	r.mu.Lock()
	runs := r.runs
	r.runs = map[worktree.Key]*run{}
	r.mu.Unlock()

	for _, rn := range runs {
		if rn.adopted {
			continue
		}
		rn.cancel()
		<-rn.done
		_ = rn.sess.Close()
	}
}

// Status is a point-in-time snapshot of one attempt for `draiverctl status`.
type Status struct {
	Key      worktree.Key
	State    project.State
	Desired  bool // in the desired (Running) set this tick
	Running  bool // this daemon owns a live session for it
	Adopted  bool // a re-adopted foreign live process (not streamed)
	Identity session.Identity
	Meter    session.Meter
}

// Snapshot reports the reconciler's current view: every attempt under the root
// with its control state, whether it is desired, and whether a session is running
// or adopted, plus its persisted identity/meter when present. It is read-only and
// safe to call concurrently with the loop.
func (r *Reconciler) Snapshot() ([]Status, error) {
	all, err := project.LoadAll(r.opt.Root)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	runs := make(map[worktree.Key]*run, len(r.runs))
	for k, v := range r.runs {
		runs[k] = v
	}
	r.mu.Unlock()

	out := make([]Status, 0, len(all))
	for _, a := range all {
		key := worktree.Key{Ticket: a.Ticket, Attempt: a.ID}
		s := Status{Key: key, State: a.State, Desired: a.State == project.Running}
		if rn, ok := runs[key]; ok {
			s.Running = !rn.adopted
			s.Adopted = rn.adopted
		}
		if id, ok := r.readIdentity(key); ok {
			s.Identity = id
		}
		if sess, err := session.Open(r.opt.Root, a.Ticket, a.ID); err == nil {
			if m, err := sess.ReadMeter(); err == nil {
				s.Meter = m
			}
			sess.Close()
		}
		out = append(out, s)
	}
	return out, nil
}

// --- client verbs (act directly on one session) ----------------------------
//
// Start/Stop/Restart are the imperative overrides the `draiver ctl` client
// exposes — the "systemctl" verbs to the reconcile loop's "PID 1". They act
// directly on a single attempt's session rather than through the desired/actual
// diff, which is what makes a hand-driven session possible without a running
// daemon (the doc's "act directly on a session for early dev"). They are not the
// scheduler: Start does not touch the run table, and none of them consult the
// desired set. Do not point them at an attempt a live daemon is already
// supervising — Tier 0 has no daemon IPC to coordinate the two.

// Start brings up a single attempt's session and blocks in the foreground,
// dispatching its stream through the very same watch+gate+protocol pipeline the
// daemon's admit uses, until the session exits on its own or ctx is cancelled.
// It is a faithful single-session daemon: the cold-start brief is injected, tool
// use is gated, and the stream is metered and promoted to the durable log — the
// "hand-driven handle to test the lower layers against."
//
// observe, if non-nil, receives every event before it is dispatched, so a caller
// can render the live stream (the CLI prints it). On return — whether the session
// exited or ctx was cancelled — the session is reaped but its id is kept on disk,
// so a later Start Resumes the same cattle handle from a fresh brief (that is what
// Restart is). Start does not register the session in the run table; it is a
// standalone driver, not part of the reconcile diff.
func (r *Reconciler) Start(ctx context.Context, ticket, attempt string, observe func(agent.Event)) error {
	a, err := project.LoadAttempt(r.opt.Root, ticket, attempt)
	if err != nil {
		return err
	}
	key := worktree.Key{Ticket: ticket, Attempt: attempt}

	w, err := r.bringUp(ctx, a)
	if err != nil {
		return err
	}
	// Reap (clearing the now-stale pid) then release the store on any exit path —
	// a clean session end, a gate halt, or a ctx cancellation. Kill is idempotent,
	// so a session a gate already halted is a no-op here.
	defer w.sess.Close()
	defer func() { _ = w.handle.Kill() }()

	if err := protocol.InjectBrief(ctx, r.opt.Root, ticket, attempt, w.handle); err != nil {
		r.opt.Logf("reconcile: inject brief %s/%s: %v", ticket, attempt, err)
	}

	stream := w.handle.Stream()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-stream:
			if !ok {
				return nil
			}
			if observe != nil {
				observe(ev)
			}
			r.dispatch(ctx, key, w.protoGate, w.permGate, w.watcher, ev)
		}
	}
}

// StopResult reports what Stop did, so the client can print a truthful message.
type StopResult struct {
	// PID is the process Stop found on record (0 if none / already cleared).
	PID int
	// Signaled is true when a live process was told to stop.
	Signaled bool
	// AlreadyStopped is true when there was no live process to signal.
	AlreadyStopped bool
}

// Stop reaps an attempt's recorded session by signalling its process, then clears
// the now-stale pid from session.json while keeping the session id, worktree, and
// log intact — the cattle handle survives for a later Start/Restart to Resume.
// It works on any recorded session regardless of who started it (a foreground
// Start, the daemon, or a re-adopted foreign process), because all it needs is
// the pid on disk. Stopping an attempt with no session, or one already stopped,
// is not an error: the goal state (not running) already holds.
func (r *Reconciler) Stop(ticket, attempt string) (StopResult, error) {
	sess, err := session.Open(r.opt.Root, ticket, attempt)
	if err != nil {
		return StopResult{}, err
	}
	defer sess.Close()

	id, err := sess.ReadIdentity()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return StopResult{AlreadyStopped: true}, nil // never had a session
		}
		return StopResult{}, err
	}

	res := StopResult{PID: id.PID}
	if id.PID != 0 && r.opt.Proc.Alive(id.PID) {
		if err := r.opt.Proc.Terminate(id.PID); err != nil {
			return res, fmt.Errorf("reconcile: stop %s/%s (pid %d): %w", ticket, attempt, id.PID, err)
		}
		res.Signaled = true
	} else {
		res.AlreadyStopped = true
	}

	if id.PID != 0 {
		id.PID = 0
		if err := sess.WriteIdentity(id); err != nil {
			return res, fmt.Errorf("reconcile: clear pid %s/%s: %w", ticket, attempt, err)
		}
	}
	return res, nil
}

// Restart reaps the current session and brings it back up fresh from a new brief
// — the doc's "context refresh" (kill + resume the same cattle handle,
// re-injecting the cold-start). It is Stop followed by Start, so it blocks in the
// foreground exactly like Start.
func (r *Reconciler) Restart(ctx context.Context, ticket, attempt string, observe func(agent.Event)) error {
	if _, err := r.Stop(ticket, attempt); err != nil {
		return err
	}
	return r.Start(ctx, ticket, attempt, observe)
}
