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
//   - Gate   — the same goroutine runs the enforcement seams. On each
//     tool-permission callback: the protocol gate (withhold an edit until its
//     rationale is logged, internal/protocol) then the permission gate (allow, or
//     escalate-and-halt, internal/gate). On each metered usage frame: the context
//     auto-stop (escalate-and-halt a session that crosses the context-window
//     limit, internal/limit) — the drvctl-012 runaway backstop.
//   - Retire — an attempt that left the desired set is stopped. A blocked one
//     (Needs-me) is parked with its worktree kept warm, so a later `resolve` —
//     which flips it back to Running — re-admits it via Resume with no work lost.
//     A terminal one (Review/Done) has its worktree reclaimed only when the
//     checkout is clean; a dirty checkout is kept warm instead, so uncommitted
//     work is never silently lost and a reopened Review resumes where the session
//     stopped (drvctl-014). Every terminal outcome is recorded to the log.
//
// The context auto-stop is the one narrow slice of budget enforcement that lands
// at Tier 0 (a runaway session had to be survivable first). Full token/$ slice
// budgets, respawn policy, the progress watchdog, StartLimit ceilings, the
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
	"path/filepath"
	"sync"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/gate"
	"github.com/Dawil/draiver/internal/limit"
	"github.com/Dawil/draiver/internal/manage"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/protocol"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/watch"
	"github.com/Dawil/draiver/internal/worktree"
)

// defaultAdapter is the adapter name assumed for an attempt whose attempt.md
// records none — Claude Code is the first and reference adapter (Tier 0).
const defaultAdapter = "claude-code"

// defaultResumeConfirm is how long bringUp waits for a Resumed session to come
// online before falling through to a fresh spawn. Long enough that a live resume
// replaying a large transcript still confirms in time (a false timeout would
// discard a resumable conversation), short enough that a genuinely dead session
// id self-heals promptly instead of hanging the bring-up.
const defaultResumeConfirm = 10 * time.Second

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

// Options configures a Reconciler. Root, Adapters, and Actor are required; the
// rest have sensible defaults.
type Options struct {
	// Root is the ticket data root — the source of truth the desired set is
	// derived from and the durable log is appended to.
	Root store.Root

	// DefaultRepo is the fallback local repo path used for an attempt that records
	// none in its attempt.md — the `ctl --repo` single-repo default that preserved
	// dogfooding before repo binding moved to the ticket (drvctl-015). Optional: an
	// attempt with a recorded repo ignores it, and an attempt with neither a
	// recorded repo nor this fallback fails its own admit (never the whole tick).
	// The reconciler no longer takes a single injected worktree.Manager; it derives
	// one per repo on demand and caches it (see managerFor).
	DefaultRepo string

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

	// ContextLimit is the context-window auto-stop threshold in tokens: a session
	// whose live context fill crosses it is halted and the stop recorded as an
	// escalation (internal/limit). Zero or negative disables the backstop.
	ContextLimit int

	// Proc probes and stops foreign session processes on restart. Zero value
	// defaults to OSProc{}.
	Proc Proc

	// ResumeConfirm bounds how long bringUp waits for a Resumed session to come
	// online (emit its first system/init frame) before concluding the recorded
	// session id is no longer resumable and falling through to a fresh spawn — the
	// cascade's self-heal (drvctl-016). Only adapters that implement agent.Onliner
	// are confirmed; others are assumed online. Zero defaults to
	// defaultResumeConfirm.
	ResumeConfirm time.Duration

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

	// wtMu guards wtCache, the per-repo worktree.Manager cache. Repo binding is
	// per-ticket now (drvctl-015), so one controller drives attempts across many
	// repos; each admitted attempt's repo path is resolved to a Manager once and
	// reused (the Manager is stateless, so caching is purely to avoid re-deriving
	// the base). Keyed by the cleaned absolute repo path.
	wtMu    sync.Mutex
	wtCache map[string]*worktree.Manager
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

	// repo is the resolved local repo path this attempt's worktree was cut from,
	// carried so retire can reclaim the checkout via the right per-repo Manager
	// (drvctl-015). Empty only if the repo could not be resolved.
	repo string
}

// New validates opt, applies defaults, and returns a Reconciler with an empty run
// table.
func New(opt Options) (*Reconciler, error) {
	switch {
	case opt.Root.Dir == "":
		return nil, errors.New("reconcile: data root is required")
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
	if opt.ResumeConfirm <= 0 {
		opt.ResumeConfirm = defaultResumeConfirm
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	return &Reconciler{opt: opt, runs: map[worktree.Key]*run{}, wtCache: map[string]*worktree.Manager{}}, nil
}

// repoFor resolves the local repo path an attempt's worktree is cut from: its
// own recorded path, else the DefaultRepo fallback. An attempt with neither is
// an error — surfaced to the caller (admit/Adopt) as a per-attempt failure, so
// it fails just that attempt rather than the whole tick.
func (r *Reconciler) repoFor(a project.Attempt) (string, error) {
	repo := a.Repo
	if repo == "" {
		repo = r.opt.DefaultRepo
	}
	if repo == "" {
		return "", fmt.Errorf("attempt %s/%s records no repo path and no --repo fallback is set", a.Ticket, a.ID)
	}
	return repo, nil
}

// managerFor returns the worktree.Manager for a repo path, deriving and caching
// one per repo (keyed by the cleaned absolute path). NewManager fails if the
// path is missing or not a git working tree — the caller wraps that into a
// per-attempt, actionable error. Managers are stateless, so the cache only saves
// re-deriving the managed base; a concurrent double-derive would be harmless but
// the lock keeps it single.
func (r *Reconciler) managerFor(repo string) (*worktree.Manager, error) {
	abs, err := filepath.Abs(repo)
	if err != nil {
		return nil, fmt.Errorf("resolve repo path %q: %w", repo, err)
	}
	abs = filepath.Clean(abs)

	r.wtMu.Lock()
	defer r.wtMu.Unlock()
	if m, ok := r.wtCache[abs]; ok {
		return m, nil
	}
	m, err := worktree.NewManager(abs)
	if err != nil {
		return nil, err
	}
	r.wtCache[abs] = m
	return m, nil
}

// Run reconciles the fleet until ctx is cancelled: Adopt once to rebuild state
// from disk, an immediate first Tick, then a Tick every interval. Cancellation
// drains — the ingest goroutines stop but the sessions are left running, so a
// later daemon start re-adopts them. It returns nil on a clean (cancelled)
// shutdown, or the error from Adopt (the one failure fatal to starting up).
func (r *Reconciler) Run(ctx context.Context, interval time.Duration, ctrl Controller) error {
	// Claim PID 1 for this data root: record the controller identity so the
	// imperative client verbs can find (and hand off to) this daemon, and clear it
	// on a clean exit. A crash leaves the record behind, but liveController probes
	// the pid, so a stale record never passes for a running daemon (drvctl-016).
	if err := WriteController(r.opt.Root, ctrl); err != nil {
		return fmt.Errorf("reconcile: claim controller: %w", err)
	}
	defer func() {
		if err := RemoveController(r.opt.Root); err != nil {
			r.opt.Logf("reconcile: release controller: %v", err)
		}
	}()

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
	// whether the worktree is a candidate for reclamation (terminal) or kept warm
	// (blocked); retire further gates reclamation on a clean checkout.
	for _, key := range current {
		if _, ok := desired[key]; ok {
			continue
		}
		r.retire(ctx, key, r.stateOf(key))
	}
	return nil
}

// desired returns the attempts that should have a live session — those that are
// both Running *and* enabled. Needs-me (blocked on a human), Review (a claim
// awaiting ratification), and Done (closed) are all not desired; and Running is
// necessary but not sufficient. Being in Running means "a human could pick this
// up," not "the supervisor will auto-spawn an agent on it now" — so an attempt
// must be explicitly enabled to join the supervised fleet (systemd's
// enable/disable: in-fleet vs parked). The default is disabled, so an idle repo
// full of Running tickets stays quiet until each is opted in.
func (r *Reconciler) desired() (map[worktree.Key]project.Attempt, error) {
	all, err := project.LoadAll(r.opt.Root)
	if err != nil {
		return nil, err
	}
	// The imperative half of the desired set (drvctl-016): a `ctl start` writes a
	// desired-marker stamped with the live controller's nonce. Read that nonce once
	// so the loop can honour a matching marker and sweep any other — a marker left
	// by a previous daemon boot (different nonce) or by a daemon that has since gone
	// away is stale, which is what makes an imperative start transient.
	ctrl, ctrlLive := r.liveController()

	out := make(map[worktree.Key]project.Attempt)
	for _, a := range all {
		if a.State != project.Running {
			continue
		}
		key := worktree.Key{Ticket: a.Ticket, Attempt: a.ID}
		if a.Enabled {
			// Declaratively desired — the durable enable bit needs no marker.
			out[key] = a
			continue
		}
		m, ok, err := readDesiredMarker(r.opt.Root, a.Ticket, a.ID)
		if err != nil {
			r.opt.Logf("reconcile: read desired marker %s/%s: %v", a.Ticket, a.ID, err)
			continue
		}
		if !ok {
			continue
		}
		if ctrlLive && m.Nonce == ctrl.Nonce {
			out[key] = a
			continue
		}
		// Stale marker (no live controller, or a different boot's nonce): sweep it so
		// the imperative start does not outlive the daemon it was handed to.
		if err := removeDesiredMarker(r.opt.Root, a.Ticket, a.ID); err != nil {
			r.opt.Logf("reconcile: sweep desired marker %s/%s: %v", a.Ticket, a.ID, err)
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
	limitGate *limit.Gate
	watcher   *watch.Watcher
	repo      string // resolved repo path, recorded on the run for retire

	// spawned reports whether bringUp started a fresh session (true) rather than
	// resuming the recorded one (false). It drives the brief-on-reset coupling:
	// the cold-start brief is (re)injected iff the session layer (L0) was
	// discarded — i.e. a fresh spawn — so a resume continues its conversation
	// without being force-fed a brief (drvctl-016).
	spawned bool
}

// bringUp opens an attempt's session store, binds a manage.Handle, brings the
// session up via the self-heal cascade (below), snapshots the protocol-gate
// baseline, and constructs the permission gate and watcher — everything needed to
// consume the live stream, but not yet consuming it. On any failure it unwinds
// cleanly (reap + close) so a failed bring-up leaves nothing half-live. The
// caller injects the cold-start brief once it is ready to consume the stream —
// but only when bringUp spawned fresh (wired.spawned), the brief-on-reset
// coupling.
//
// The cascade climbs from the highest surviving layer: if a session id is on
// record it Resumes and confirms the resume came online; a recorded id that can
// no longer be resumed launches a process that dies on arrival and never comes
// online, so bringUp reaps it and falls through to a fresh Spawn on the same
// worktree rather than looping on the dead id forever (drvctl-016). Spawn itself
// creates the worktree if it is gone, so the worktree and attempt-log rungs of
// the cascade fall out of Spawn's own idempotence.
func (r *Reconciler) bringUp(ctx context.Context, a project.Attempt) (*wired, error) {
	adapterName := a.Tool
	if adapterName == "" {
		adapterName = defaultAdapter
	}
	newAdapter, err := r.opt.Adapters(adapterName)
	if err != nil {
		return nil, fmt.Errorf("resolve adapter %q: %w", adapterName, err)
	}

	// Resolve this attempt's own repo and get-or-create its Manager. A missing or
	// non-git repo fails *this* attempt's bring-up with an actionable error naming
	// the ticket and path; admit logs it and the tick moves on to other attempts.
	repo, err := r.repoFor(a)
	if err != nil {
		return nil, err
	}
	wm, err := r.managerFor(repo)
	if err != nil {
		return nil, fmt.Errorf("attempt %s/%s repo %q is missing or not a git working tree: %w", a.Ticket, a.ID, repo, err)
	}

	sess, err := session.Open(r.opt.Root, a.Ticket, a.ID)
	if err != nil {
		return nil, err
	}

	handle, err := manage.New(sess, wm, newAdapter, manage.Config{
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
	// handle and Resume rather than Spawn a second, unrelated session. But a
	// recorded id is not proof the id is still resumable, so a resume is trusted
	// only once it comes online; otherwise the cascade falls through to a fresh
	// spawn on the same worktree (the self-heal that retires "stale id resumed
	// forever").
	resuming := false
	if id, err := sess.ReadIdentity(); err == nil && id.SessionID != "" {
		resuming = true
	}
	spawned := false
	if resuming {
		if err := handle.Resume(ctx); err != nil {
			// The recorded id could not even be launched; fall through to a fresh
			// spawn rather than failing the whole bring-up.
			r.opt.Logf("reconcile: resume %s/%s failed to launch, spawning fresh: %v", a.Ticket, a.ID, err)
			resuming = false
		} else if confirmed, cerr := r.confirmOnline(ctx, handle); cerr != nil {
			// ctx was cancelled while confirming; unwind cleanly.
			_ = handle.Kill()
			_ = sess.Close()
			return nil, cerr
		} else if !confirmed {
			// Process started but never came online — an unresumable id. Reap it and
			// fall through; the following Spawn overwrites the dead id with a fresh one.
			r.opt.Logf("reconcile: resume %s/%s never came online, spawning fresh on the same worktree", a.Ticket, a.ID)
			_ = handle.Kill()
			resuming = false
		}
	}
	if !resuming {
		if err := handle.Spawn(ctx); err != nil {
			_ = sess.Close()
			return nil, err
		}
		spawned = true
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
	limitGate := limit.New(r.opt.Root, a.Ticket, a.ID, r.opt.Actor, r.opt.ContextLimit, handle.Kill)
	watcher := watch.New(r.opt.Root, a.Ticket, a.ID, r.opt.Actor, sess, watch.ProtocolRecognizer{Ticket: a.Ticket})

	return &wired{handle: handle, sess: sess, protoGate: protoGate, permGate: permGate, limitGate: limitGate, watcher: watcher, repo: repo, spawned: spawned}, nil
}

// confirmOnline waits for a just-Resumed session to come online — to emit its
// first system/init frame — bounding the wait at ResumeConfirm. It returns
// (true, nil) once the session signals online, (false, nil) if the window
// elapses first (the caller reaps and falls through to a fresh spawn), or a
// non-nil error if ctx is cancelled while waiting (the caller aborts the
// bring-up). An adapter that cannot signal online (does not implement
// agent.Onliner, so Handle.Online is nil) is assumed online, preserving the
// pre-cascade behaviour for adapters without the capability.
func (r *Reconciler) confirmOnline(ctx context.Context, handle *manage.Handle) (bool, error) {
	online := handle.Online()
	if online == nil {
		return true, nil
	}
	select {
	case <-online:
		return true, nil
	case <-time.After(r.opt.ResumeConfirm):
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
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
	rn := &run{handle: w.handle, sess: w.sess, cancel: cancel, done: make(chan struct{}), repo: w.repo}

	r.mu.Lock()
	r.runs[key] = rn
	r.mu.Unlock()

	go r.ingest(ictx, key, rn, w.protoGate, w.permGate, w.limitGate, w.watcher, stream)

	// Inject the cold-start brief last, so the stream is already being consumed
	// when the agent starts producing — but only on a fresh spawn (L0 discarded).
	// A resume continues its own conversation and must not be force-fed a brief
	// (the brief-on-reset coupling, drvctl-016). A brief that fails to
	// build/deliver does not tear the live session down — it is logged and the
	// session works on.
	if w.spawned {
		if err := protocol.InjectBrief(ctx, r.opt.Root, a.Ticket, a.ID, w.handle); err != nil {
			r.opt.Logf("reconcile: inject brief %s/%s: %v", a.Ticket, a.ID, err)
		}
	}
	return nil
}

// ingest is the Watch+Gate goroutine for one live session: it ranges the session
// stream until it closes (the session exited) or ctx is cancelled (retire/drain),
// dispatching each event. It never removes its own run from the table — only
// retire/drain do — so a session that exits on its own leaves a spent entry that
// keeps the next tick from re-admitting it (Tier 0 has no respawn ceiling, so
// re-admitting a crashed session would be an unbounded loop).
func (r *Reconciler) ingest(ctx context.Context, key worktree.Key, rn *run, pg *protocol.Gate, permGate *gate.Gate, lg *limit.Gate, w *watch.Watcher, stream <-chan agent.Event) {
	defer close(rn.done)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-stream:
			if !ok {
				return
			}
			r.dispatch(ctx, key, pg, permGate, lg, w, ev)
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
func (r *Reconciler) dispatch(ctx context.Context, key worktree.Key, pg *protocol.Gate, permGate *gate.Gate, lg *limit.Gate, w *watch.Watcher, ev agent.Event) {
	if _, err := w.Process(ev); err != nil {
		r.opt.Logf("reconcile: watch %s/%s: %v", key.Ticket, key.Attempt, err)
	}

	// Context-window backstop: after the meter has folded this event's usage,
	// check the live context fill against the limit. A crossing records the
	// auto-stop escalation and reaps the session (which closes the stream, ending
	// this ingest); the attempt is now Needs-me, so the next tick parks it.
	if out, err := lg.Consider(ev); err != nil {
		r.opt.Logf("reconcile: limit gate %s/%s: %v", key.Ticket, key.Attempt, err)
	} else if out == limit.Stopped {
		r.opt.Logf("reconcile: context auto-stop %s/%s at %d tokens", key.Ticket, key.Attempt, ev.Usage.ContextTokens)
		return
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

// retire stops the session for key and removes it from the table, then decides
// what to do with its worktree based on the attempt's terminal state st:
//
//   - Needs-me (blocked): keep the worktree warm, untouched, so a post-resolution
//     Resume continues exactly where it left off.
//   - Review/Done (terminal) with a *dirty* checkout: keep it warm too. The branch
//     is a crash-recovery net only for *committed* work; force-removing a checkout
//     that holds uncommitted or untracked changes silently destroys them
//     (drvctl-014). Because Review is a reopenable gate (drv-002), a preserved
//     checkout also lets a reopen resume where the session stopped rather than
//     from the last commit.
//   - Review/Done (terminal) with a *clean* checkout: reclaim it. Removal is
//     non-destructive (any work is committed to the branch), so the checkout is
//     removed and the branch kept as the crash-recovery net.
//
// Every terminal outcome is recorded to the durable log, so a human or a resumed
// agent can see whether the worktree was reclaimed or preserved. An adopted
// (foreign) session is stopped by pid, the only handle Tier 0 has on it.
func (r *Reconciler) retire(ctx context.Context, key worktree.Key, st project.State) {
	// A retired attempt is no longer desired, so any imperative desired-marker it
	// carried has done its job — clear it so a later return to Running (a reopen)
	// does not resurrect the same one-shot start under the still-live daemon
	// (drvctl-016). Missing is fine.
	if err := removeDesiredMarker(r.opt.Root, key.Ticket, key.Attempt); err != nil {
		r.opt.Logf("reconcile: clear desired marker %s/%s: %v", key.Ticket, key.Attempt, err)
	}

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

	// Needs-me is parked with its worktree kept warm; only a terminal retire is a
	// candidate for reclaiming the checkout.
	if st == project.NeedsMe {
		return
	}

	// Reclamation acts on the attempt's own repo (drvctl-015). If it could not be
	// resolved (no repo recorded, its checkout already gone), keep the worktree as
	// it stands rather than guess — the process is already stopped either way.
	if rn.repo == "" {
		r.opt.Logf("reconcile: retire %s/%s: no repo resolved, leaving worktree as-is", key.Ticket, key.Attempt)
		r.recordRetire(key, st, false, "the attempt's repo path could not be resolved, so its worktree was left untouched")
		return
	}
	wm, err := r.managerFor(rn.repo)
	if err != nil {
		r.opt.Logf("reconcile: retire %s/%s: worktree manager for %q: %v", key.Ticket, key.Attempt, rn.repo, err)
		r.recordRetire(key, st, false, fmt.Sprintf("the repo %q could not be opened to reclaim the checkout (%v)", rn.repo, err))
		return
	}

	dirty, err := wm.Dirty(ctx, key)
	if err != nil {
		// Cannot prove the checkout is clean — err on the side of preserving it
		// rather than risk a silent loss, and leave it warm for inspection.
		r.opt.Logf("reconcile: dirty check %s/%s: %v", key.Ticket, key.Attempt, err)
		r.recordRetire(key, st, false, fmt.Sprintf("could not determine whether the checkout was clean (%v)", err))
		return
	}
	if dirty {
		r.recordRetire(key, st, false, "the checkout held uncommitted or untracked changes")
		return
	}

	// Clean: reclaiming is non-destructive. Drop Force so that if the tree turned
	// dirty since the check, git refuses the removal rather than clobbering it.
	if err := wm.Remove(ctx, key, worktree.RemoveOptions{}); err != nil {
		r.opt.Logf("reconcile: remove worktree %s/%s: %v", key.Ticket, key.Attempt, err)
		r.recordRetire(key, st, false, fmt.Sprintf("attempted to reclaim the clean checkout but removal failed (%v)", err))
		return
	}
	r.recordRetire(key, st, true, "the checkout was clean (all work committed to the branch)")
}

// recordRetire appends a durable note recording what retire did with the
// worktree — reclaimed it, or kept it warm and why — so the decision is visible
// on the board and to a resumed agent. It is best-effort: a note that cannot be
// written is logged operationally, never failing the retire.
func (r *Reconciler) recordRetire(key worktree.Key, st project.State, reclaimed bool, reason string) {
	var body string
	if reclaimed {
		body = fmt.Sprintf("Daemon reclaimed the worktree on retire into %s: %s.", st, reason)
	} else {
		body = fmt.Sprintf("Daemon kept the worktree warm on retire into %s: %s. It is preserved so the attempt can resume where the session stopped without losing work.", st, reason)
	}
	if _, err := ticketlog.Append(r.opt.Root, key.Ticket, key.Attempt, event.Event{
		Type:  "note",
		Actor: r.opt.Actor,
		Body:  body,
	}); err != nil {
		r.opt.Logf("reconcile: record retire %s/%s: %v", key.Ticket, key.Attempt, err)
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

	// Group the keep-set by repo so each per-repo Manager reconciles only its own
	// checkouts (drvctl-015). A managed worktree only exists where a session was
	// spawned, which also wrote session.json, so every managed checkout belongs to
	// a key in keep — iterating repos-of-keep-keys covers every repo with worktrees
	// to sweep. repoByKey feeds the resolved repo onto each adopted run for retire.
	keepByRepo := map[string][]worktree.Key{}
	repoByKey := map[worktree.Key]string{}
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
			if id.PID != 0 && r.opt.Proc.Alive(id.PID) {
				live[key] = id.PID
			}
			a, err := project.LoadAttempt(r.opt.Root, ticket, att)
			if err != nil {
				r.opt.Logf("reconcile: adopt %s/%s: load attempt: %v", ticket, att, err)
				continue
			}
			repo, err := r.repoFor(a)
			if err != nil {
				// A session exists but its repo can no longer be resolved: its
				// worktree cannot be swept, but a live pid is still re-adopted below.
				r.opt.Logf("reconcile: adopt %s/%s: %v", ticket, att, err)
				continue
			}
			keepByRepo[repo] = append(keepByRepo[repo], key)
			repoByKey[key] = repo
		}
	}

	for repo, keep := range keepByRepo {
		wm, err := r.managerFor(repo)
		if err != nil {
			// The repo is gone; its checkouts (also derived from it) are unreachable.
			// Skip its sweep rather than fail the whole adopt over one missing repo.
			r.opt.Logf("reconcile: adopt: worktree manager for %q: %v", repo, err)
			continue
		}
		if _, err := wm.Reconcile(ctx, keep); err != nil {
			return fmt.Errorf("reconcile worktrees on adopt (repo %s): %w", repo, err)
		}
	}

	r.mu.Lock()
	for key, pid := range live {
		if _, exists := r.runs[key]; exists {
			continue
		}
		r.runs[key] = &run{adopted: true, pid: pid, repo: repoByKey[key]}
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
	Enabled  bool // opted into daemon supervision
	Desired  bool // in the desired (Running AND enabled) set this tick
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
		s := Status{Key: key, State: a.State, Enabled: a.Enabled, Desired: a.State == project.Running && a.Enabled}
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

// --- client verbs -----------------------------------------------------------
//
// Start/Stop/Restart are the imperative overrides the `draiver ctl` client
// exposes — the "systemctl" verbs to the reconcile loop's "PID 1". Start is a
// control-plane action: it does not bring a session up itself (an unsupervised
// orphan that dies with the invoking shell), it hands the attempt off to a
// running `ctl up` by writing the transient desired-marker the loop reconciles
// — so the self-heal cascade and brief-on-reset coupling live in the one
// daemon-shared bringUp and the client path cannot drift from it (drvctl-016).
// Stop stays a direct reap (it only needs the recorded pid, and reaping a
// process wants no supervisor). runForeground is the pre-daemon foreground
// driver Restart still uses this phase.

// ErrNoController is returned by the imperative verbs when no `ctl up` is
// running to hand the attempt off to — the "requires PID 1" gate that keeps a
// backgrounded start from spawning an unsupervised orphan (drvctl-016).
var ErrNoController = errors.New("no running `ctl up` (start the supervisor with `ctl up` first)")

// StartResult reports how an imperative Start was handed off, so the client can
// print a truthful message (which supervisor now owns the attempt).
type StartResult struct {
	// ControllerPID is the pid of the running `ctl up` the attempt was handed to.
	ControllerPID int
}

// Start hands one attempt off to a running `ctl up` to bring up in the
// background, rather than driving a session itself. It requires a live
// controller (ErrNoController otherwise) and writes a transient desired-marker
// stamped with that controller's nonce; the daemon's next tick unions the
// marker into its desired set and admits the attempt through the same cascade
// (resume the recorded session, else spawn fresh) and brief-on-reset coupling
// its enabled fleet uses. The marker is transient: it is swept if the daemon
// restarts (new nonce), so an imperative start dies with its supervisor —
// distinct from `enable`, the durable, restart-surviving opt-in. Streaming is
// no longer part of Start; watch the handed-off session with `ctl logs -f`.
func (r *Reconciler) Start(ticket, attempt string) (StartResult, error) {
	if _, err := project.LoadAttempt(r.opt.Root, ticket, attempt); err != nil {
		return StartResult{}, err
	}
	ctrl, ok := r.liveController()
	if !ok {
		return StartResult{}, ErrNoController
	}
	m := desiredMarker{Nonce: ctrl.Nonce, Stamp: r.opt.Now()}
	if err := writeDesiredMarker(r.opt.Root, ticket, attempt, m); err != nil {
		return StartResult{}, err
	}
	return StartResult{ControllerPID: ctrl.PID}, nil
}

// runForeground brings up a single attempt's session and blocks in the
// foreground, dispatching its stream through the very same watch+gate+protocol
// pipeline the daemon's admit uses, until the session exits on its own or ctx is
// cancelled. It climbs the same self-heal cascade admit does (bringUp): a
// resumable session is Resumed and continued as-is; only a fresh spawn is
// cold-started from the brief. observe, if non-nil, receives every event before
// dispatch so a caller can render the live stream. On return the session is
// reaped but its id kept on disk, so a later bring-up Resumes the same cattle
// handle. It does not register the session in the run table — a standalone
// driver, not part of the reconcile diff. Restart still uses it this phase;
// Start has moved to the daemon-handoff above (drvctl-016).
func (r *Reconciler) runForeground(ctx context.Context, ticket, attempt string, observe func(agent.Event)) error {
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

	// Brief-on-reset: (re)inject the cold-start brief only when bringUp spawned a
	// fresh session (L0 discarded). A resume continues its conversation without a
	// re-brief (drvctl-016).
	if w.spawned {
		if err := protocol.InjectBrief(ctx, r.opt.Root, ticket, attempt, w.handle); err != nil {
			r.opt.Logf("reconcile: inject brief %s/%s: %v", ticket, attempt, err)
		}
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
			r.dispatch(ctx, key, w.protoGate, w.permGate, w.limitGate, w.watcher, ev)
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

// FlushLevel selects how deep restart's teardown reaps before the start cascade
// climbs back — a point on the degree axis of drvctl-016's state stack. The
// levels are ordinal and cumulative: a deeper level implies every shallower
// flush. Each step discards one more volatile layer stacked on the durable ticket
// floor (L3):
//
//	FlushNone      reap the process only; the cascade Resumes the same session (L0 kept).
//	FlushSession   + discard the session conversation (L0); the cascade spawns a
//	               fresh session on the surviving worktree, cold-started from the brief.
//	FlushWorktree  + discard the worktree checkout and branch (L1); the cascade
//	               rebuilds the worktree from HEAD before the fresh spawn.
//	FlushAttempt   + discard the attempt (L2) — this FORKS a new attempt (new id,
//	               `from` provenance) and climbs into it; the prior attempt's log is
//	               preserved untouched as an immutable record.
type FlushLevel int

const (
	FlushNone FlushLevel = iota
	FlushSession
	FlushWorktree
	FlushAttempt
)

// RestartResult reports what a Restart flushed and which attempt the start
// cascade climbed back into — the same attempt for an in-place restart, or the
// new fork's id when the depth reached FlushAttempt. Restart hands it to its
// announce callback so a caller can report the plan (in particular a fork's new
// id) before Start blocks.
type RestartResult struct {
	Level   FlushLevel
	Ticket  string
	Attempt string // the attempt the cascade brought up (the fork's id for FlushAttempt)
	Forked  bool   // true when FlushAttempt created a new attempt
	From    string // the parent attempt id, set when Forked
}

// Restart reaps the current session (Stop) then brings the attempt back up
// through the foreground driver (runForeground), blocking until it exits. level
// selects how deep the teardown flushes before the start cascade climbs back —
// the degree axis of drvctl-016 (see FlushLevel). FlushNone is the clean
// "continue" the old hybrid restart failed to be: it Resumes the same session and
// does not re-brief. FlushAttempt is the one point on the axis where restart
// stops mutating in place and instead forks a new attempt, bringing *that* one up
// fresh.
//
// Restart still drives its session in the foreground this phase; the imperative
// Start verb has already moved to the daemon handoff (drvctl-016 Phase C1), and
// Restart follows in C2.
//
// announce, if non-nil, is called once the target attempt is fixed (after any
// flush/fork) but before the session blocks, so a caller can surface the plan —
// notably a fork's new id — up front rather than only after the session exits.
func (r *Reconciler) Restart(ctx context.Context, ticket, attempt string, level FlushLevel, announce func(RestartResult), observe func(agent.Event)) error {
	// The shallowest teardown always happens: reap the running process, keeping the
	// durable layers for the flush/cascade to act on.
	if _, err := r.Stop(ticket, attempt); err != nil {
		return err
	}

	res := RestartResult{Level: level, Ticket: ticket, Attempt: attempt}

	switch {
	case level >= FlushAttempt:
		// Stop mutating in place and branch: fork a new attempt and climb into it.
		newID, err := r.forkAttempt(ticket, attempt)
		if err != nil {
			return err
		}
		r.recordFork(ticket, attempt, newID)
		res.Attempt, res.Forked, res.From = newID, true, attempt
	case level >= FlushWorktree:
		a, err := project.LoadAttempt(r.opt.Root, ticket, attempt)
		if err != nil {
			return err
		}
		if err := r.flushSession(ticket, attempt); err != nil {
			return err
		}
		if err := r.flushWorktree(ctx, a); err != nil {
			return err
		}
		r.recordFlush(ticket, attempt, FlushWorktree)
	case level >= FlushSession:
		if err := r.flushSession(ticket, attempt); err != nil {
			return err
		}
		r.recordFlush(ticket, attempt, FlushSession)
	}

	if announce != nil {
		announce(res)
	}
	return r.runForeground(ctx, res.Ticket, res.Attempt, observe)
}

// flushSession discards an attempt's session conversation (L0): it clears the
// recorded session id so the start cascade cannot Resume the old conversation and
// instead Spawns a fresh session on the surviving worktree (which then cold-starts
// from the brief). The pid was already cleared by the preceding Stop. An attempt
// with no session on record — or one whose id is already empty — is a no-op: there
// is no conversation to discard. Only the id is cleared; the meter and stream tee
// carry across exactly as they do when the Phase-A cascade falls through a dead id
// to a fresh spawn.
func (r *Reconciler) flushSession(ticket, attempt string) error {
	sess, err := session.Open(r.opt.Root, ticket, attempt)
	if err != nil {
		return err
	}
	defer sess.Close()

	id, err := sess.ReadIdentity()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // no session on record — nothing to flush
		}
		return err
	}
	if id.SessionID == "" {
		return nil
	}
	id.SessionID = ""
	id.PID = 0
	return sess.WriteIdentity(id)
}

// flushWorktree discards an attempt's worktree (L1): it force-removes the checkout
// and deletes the per-attempt branch, so the start cascade rebuilds a fresh
// worktree from HEAD rather than re-attaching to the surviving branch. The force
// is deliberate — --new-worktree is an explicit request to discard the checkout,
// uncommitted work and all. A worktree that was never created is tolerated as a
// no-op by Remove.
func (r *Reconciler) flushWorktree(ctx context.Context, a project.Attempt) error {
	repo, err := r.repoFor(a)
	if err != nil {
		return err
	}
	wm, err := r.managerFor(repo)
	if err != nil {
		return fmt.Errorf("attempt %s/%s repo %q is missing or not a git working tree: %w", a.Ticket, a.ID, repo, err)
	}
	key := worktree.Key{Ticket: a.Ticket, Attempt: a.ID}
	if err := wm.Remove(ctx, key, worktree.RemoveOptions{Force: true, DeleteBranch: true}); err != nil {
		return fmt.Errorf("flush worktree %s/%s: %w", a.Ticket, a.ID, err)
	}
	return nil
}

// forkAttempt branches a new attempt off parent (the L2 flush): it inherits the
// parent's tool/model/repo and records `from` provenance, leaving the parent's log
// untouched as the immutable record of the path taken so far. It returns the new
// attempt's id, which the start cascade then cold-starts fresh (no session, no
// worktree — Spawn builds both). Repo is inherited so the fork targets the same
// working tree, mirroring `attempt new --from`.
func (r *Reconciler) forkAttempt(ticket, parent string) (string, error) {
	pm, err := attempt.LoadMeta(r.opt.Root, ticket, parent)
	if err != nil {
		return "", err
	}
	m, err := attempt.Create(r.opt.Root, ticket, attempt.New{
		Tool:  pm.Tool,
		Model: pm.Model,
		Repo:  pm.Repo,
		Actor: r.opt.Actor,
		From:  parent,
	})
	if err != nil {
		return "", err
	}
	return m.ID, nil
}

// recordFork appends a note to the parent attempt's log recording that a restart
// forked a new attempt from it, so the branch point — the one place restart lands
// on a *different* attempt — is visible on the board and to a resumed agent.
// Best-effort: a note that cannot be written is logged operationally, never
// failing the restart.
func (r *Reconciler) recordFork(ticket, parent, child string) {
	body := fmt.Sprintf("Forked a new attempt %s/%s from this one (restart --new-attempt); this attempt's log is preserved as an immutable record of the path taken so far.", ticket, child)
	if _, err := ticketlog.Append(r.opt.Root, ticket, parent, event.Event{
		Type:  "note",
		Actor: r.opt.Actor,
		Body:  body,
	}); err != nil {
		r.opt.Logf("reconcile: record fork %s/%s: %v", ticket, parent, err)
	}
}

// recordFlush appends a note recording an in-place restart's teardown depth, so a
// destructive reset (especially --new-worktree, which discards uncommitted work) is
// visible on the board rather than silent. FlushNone and FlushAttempt are recorded
// elsewhere (a bare restart is an unremarkable continue; a fork is recorded on the
// parent). Best-effort, like recordFork.
func (r *Reconciler) recordFlush(ticket, attempt string, level FlushLevel) {
	var what string
	switch level {
	case FlushSession:
		what = "discarded the session conversation (restart --new-session); a fresh session will cold-start from the brief on the same worktree"
	case FlushWorktree:
		what = "discarded the session conversation and the worktree checkout+branch (restart --new-worktree); the worktree will be rebuilt from HEAD and the session cold-started from the brief"
	default:
		return
	}
	if _, err := ticketlog.Append(r.opt.Root, ticket, attempt, event.Event{
		Type:  "note",
		Actor: r.opt.Actor,
		Body:  "Restart flush: " + what + ".",
	}); err != nil {
		r.opt.Logf("reconcile: record flush %s/%s: %v", ticket, attempt, err)
	}
}
