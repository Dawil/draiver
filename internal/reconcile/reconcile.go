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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/completion"
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

	// healthMu guards health, the per-attempt set of currently-active operational
	// error classes (keyed by class string). It is edge-triggering state for the
	// ctl.jsonl health log: Tick diffs each attempt's observation against it to emit
	// error-start/error-end transitions (see health.go). In-memory and rebuildable —
	// a restart re-observes and re-emits on the next tick — matching the loop's
	// "the only runtime state is session/ + the in-memory table" model.
	healthMu sync.Mutex
	health   map[worktree.Key]map[string]bool
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
	return &Reconciler{
		opt:     opt,
		runs:    map[worktree.Key]*run{},
		wtCache: map[string]*worktree.Manager{},
		health:  map[worktree.Key]map[string]bool{},
	}, nil
}

// errNoRepoBound marks the one admit failure that is turned into a durable
// escalation rather than only logged (drvctl-017): the attempt records no repo
// and no --repo fallback resolves. repoFor wraps it so admit → Tick can recognize
// the case with errors.Is; every other admit failure (e.g. a bad git tree) still
// only Logf-s. Routing all admit failures through one escalate path is a trivial
// extension from here, deliberately left out of this ticket's scope.
var errNoRepoBound = errors.New("attempt records no repo path and no --repo fallback is set")

// repoFor resolves the local repo path an attempt's worktree is cut from: its
// own recorded path, else the DefaultRepo fallback. An attempt with neither is
// an error — surfaced to the caller (admit/Adopt) as a per-attempt failure, so
// it fails just that attempt rather than the whole tick. The error wraps
// errNoRepoBound so admit can escalate this specific case (drvctl-017).
func (r *Reconciler) repoFor(a project.Attempt) (string, error) {
	repo := a.Repo
	if repo == "" {
		repo = r.opt.DefaultRepo
	}
	if repo == "" {
		return "", fmt.Errorf("attempt %s/%s: %w", a.Ticket, a.ID, errNoRepoBound)
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

	// Admit: desired but not running. A key already in the table is normally left
	// alone; the exception is a spent run an intentional reap cleared while the
	// attempt is still desired (readmittable) — the daemon reaps the spent entry
	// and re-admits, the seam a client `restart` (and a bare `ctl stop` on a
	// still-enabled attempt) rides on (drvctl-016).
	for key, a := range desired {
		if currentSet[key] {
			if !r.readmittable(key) {
				// A session already live for this attempt is healthy: clear any
				// operational error class it was carrying (edge → error-end).
				r.reportHealth(key, "", "")
				continue
			}
			r.reapSpentRun(key)
		}
		if err := r.admit(ctx, a); err != nil {
			// A missing repo is a human-actionable block, not a transient hiccup:
			// surface it on the board as an escalation instead of only logging it,
			// where it would silently stall (drvctl-017). Every other admit failure
			// stays operational — recorded as an edge-triggered health transition on
			// the attempt's ctl.jsonl (drvctl-027) so a wedge is observable, and
			// logged for the daemon's own operator stream.
			if errors.Is(err, errNoRepoBound) {
				r.escalateNoRepo(a)
			} else {
				r.reportHealth(key, classifyAdmit(err), err.Error())
				r.opt.Logf("reconcile: admit %s/%s: %v", key.Ticket, key.Attempt, err)
			}
		} else {
			// Admit succeeded: whatever class was wedging this attempt has cleared.
			r.reportHealth(key, "", "")
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

	// An attempt that left the desired set (disabled, retired, or blocked) is no
	// longer supervised, so any operational error class it was carrying is over from
	// the daemon's view: close it out with an error-end rather than strand the record
	// (and the webui red dot) on a lone error-start (drvctl-027).
	r.sweepHealth(desired)
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

	go r.ingest(ictx, key, rn, w.protoGate, w.permGate, w.limitGate, w.completeGate, w.watcher, stream)

	// Drive the session last, so the stream is already being consumed when the
	// agent starts producing (bringUp-vs-resume brief decision lives in drive).
	r.drive(ctx, a, w)
	return nil
}

// noRepoMarker is the stable phrase embedded in the no-repo escalation body; it
// doubles as the de-dup key so admit — which runs every tick — never appends a
// second escalation while one is already open (mirroring how the gates check the
// log tail before acting).
const noRepoMarker = "no repo path is bound to this attempt"

// noRepoEscalationBody renders the human-facing escalation for an attempt that
// records no repo and resolves no --repo fallback. It names the two ways to
// unblock it — either sanctioned by drvctl-015 — and always contains noRepoMarker.
func noRepoEscalationBody() string {
	return fmt.Sprintf("Supervisor could not start this attempt: %s, so no worktree can be cut for it.\n\n"+
		"Set `repo:` in the attempt's `attempt.md` to the local git working tree it targets, "+
		"or restart the daemon with a `ctl up --repo <path>` fallback. "+
		"Once a repo resolves, the attempt resumes from the log.", noRepoMarker)
}

// escalateNoRepo turns a repo-less attempt's failed admit into a durable
// escalation, flipping it to Needs-me so it lands on the board with an actionable
// ask instead of silently stalling Running+enabled (drvctl-017). It is
// idempotent: if an unresolved no-repo escalation is already open it only logs,
// so a resolve that does not actually bind a repo (which returns the attempt to
// Running and re-admits it) can re-escalate, but a still-open one is never
// duplicated. Best-effort — a write failure is logged, never failing the tick.
func (r *Reconciler) escalateNoRepo(a project.Attempt) {
	if att, err := project.LoadAttempt(r.opt.Root, a.Ticket, a.ID); err == nil {
		for _, e := range att.OpenEscalations {
			if strings.Contains(e.Body, noRepoMarker) {
				r.opt.Logf("reconcile: admit %s/%s: no repo bound (escalation already open)", a.Ticket, a.ID)
				return
			}
		}
	}
	if _, err := ticketlog.Append(r.opt.Root, a.Ticket, a.ID, event.Event{
		Type:  "escalation",
		Actor: r.opt.Actor,
		Body:  noRepoEscalationBody(),
	}); err != nil {
		r.opt.Logf("reconcile: record no-repo escalation %s/%s: %v", a.Ticket, a.ID, err)
		return
	}
	r.opt.Logf("reconcile: escalated %s/%s: no repo bound", a.Ticket, a.ID)
}

// ingest is the Watch+Gate goroutine for one live session: it ranges the session
// stream until it closes (the session exited) or ctx is cancelled (retire/drain),
// dispatching each event. It never removes its own run from the table — only
// retire/drain do — so a session that exits on its own leaves a spent entry that
// keeps the next tick from re-admitting it (Tier 0 has no respawn ceiling, so
// re-admitting a crashed session would be an unbounded loop).
func (r *Reconciler) ingest(ctx context.Context, key worktree.Key, rn *run, pg *protocol.Gate, permGate *gate.Gate, lg *limit.Gate, cg *completion.Gate, w *watch.Watcher, stream <-chan agent.Event) {
	defer close(rn.done)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-stream:
			if !ok {
				return
			}
			r.dispatch(ctx, key, pg, permGate, lg, cg, w, ev)
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
func (r *Reconciler) dispatch(ctx context.Context, key worktree.Key, pg *protocol.Gate, permGate *gate.Gate, lg *limit.Gate, cg *completion.Gate, w *watch.Watcher, ev agent.Event) {
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

	// Turn-end guard: a session that ends a success turn without having claimed
	// review or filed an escalation is nudged once, then escalated-and-halted to a
	// human — so it can never silently stop at "done" and strand the attempt in
	// Running + enabled (drvctl-022). watch.Process above has already promoted any
	// review/escalate the agent ran this turn, so the gate reads a log that reflects
	// a genuine hand-off. An Escalated outcome reaped the session; nothing more to do.
	if out, err := cg.Consider(ctx, ev); err != nil {
		r.opt.Logf("reconcile: completion gate %s/%s: %v", key.Ticket, key.Attempt, err)
	} else if out == completion.Escalated {
		r.opt.Logf("reconcile: completion auto-stop %s/%s: success turn with no review/escalation filed", key.Ticket, key.Attempt)
		return
	} else if out == completion.Nudged {
		r.opt.Logf("reconcile: completion nudge %s/%s: reminded to claim review or escalate", key.Ticket, key.Attempt)
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

// readmittable reports whether a run already in the table should be reaped and
// admitted afresh this tick — the seam a client `restart` (and a bare `ctl stop`
// on a still-desired attempt) rides on. It is called only for keys already in the
// desired set, so desired-ness is a given; what it adds is telling an intentional
// reap apart from a crash. Two conditions hold:
//
//   - the run is spent (its ingest goroutine has returned, so the session
//     process is gone) — a live run is left strictly alone;
//   - the on-disk pid is 0, the mark of an intentional reap (Stop/restart clear
//     it). A crash or clean exit leaves the dead pid on record, so the Tier-0
//     "no respawn on crash" invariant still holds. The other pid==0 path — a gate
//     or context-limit auto-stop — moves the attempt out of desired, so it never
//     reaches here (decision #29).
//
// No desired-marker check is needed: the desired() gate already filtered on it
// (a `ctl stop` removes the marker, so a purely-imperative attempt leaves desired
// and is not re-admitted; an enabled attempt is re-admitted on the enable bit
// alone — a bare stop on it is transient by design). An adopted (foreign) run has
// no ingest to spend and no handle to re-drive, so it is never re-admitted here.
func (r *Reconciler) readmittable(key worktree.Key) bool {
	r.mu.Lock()
	rn := r.runs[key]
	r.mu.Unlock()
	if rn == nil || rn.adopted {
		return false
	}
	select {
	case <-rn.done:
	default:
		return false // still live
	}
	id, ok := r.readIdentity(key)
	if !ok || id.PID != 0 {
		return false
	}
	return true
}

// reapSpentRun removes a spent run from the table and releases its resources, so
// the following admit brings the attempt up afresh. It is only called for a run
// readmittable already confirmed spent (its done channel closed), so the receive
// on done does not block. Kill is idempotent — the process is already gone — and
// only clears the now-stale handle state.
func (r *Reconciler) reapSpentRun(key worktree.Key) {
	r.mu.Lock()
	rn := r.runs[key]
	delete(r.runs, key)
	r.mu.Unlock()
	if rn == nil {
		return
	}
	rn.cancel()
	if err := rn.handle.Kill(); err != nil {
		r.opt.Logf("reconcile: reap spent %s/%s: %v", key.Ticket, key.Attempt, err)
	}
	<-rn.done
	_ = rn.sess.Close()
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
