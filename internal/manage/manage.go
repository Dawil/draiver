// Package manage binds one attempt's coding-agent adapter, its git worktree, and
// its persisted session identity into a single live-session controller — the
// "agents are cattle" mechanic in one type.
//
// A Handle owns the lifecycle of one session for one attempt. The load-bearing
// idea is that the session id is a durable cattle handle, not a precious process:
//
//   - Spawn creates the attempt's worktree, starts a fresh agent process, and
//     persists its session id (and pid, worktree, birth time) to session.json.
//   - Kill reaps the process but keeps the session id and the ticket log intact —
//     only the now-stale pid is cleared. The context lives in the log, not the
//     dead process.
//   - Resume reloads the session id from session.json and spins a fresh process
//     with the agent's --resume, re-attaching to the same worktree. The reloaded
//     process cold-starts from `brief` (enforced by a later ticket).
//
// manage owns session.json (identity); it deliberately does not touch the meter
// or the stream tee — those belong to watch.Watcher, which the caller runs over
// the channel Handle.Stream() exposes. On every Spawn or Resume the live process
// is fresh, so Stream() returns a new channel and the caller re-attaches its
// watcher to it.
package manage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/worktree"
)

// Config is the declarative description of the session a Handle drives — the
// slice of the "unit file" manage needs beyond the primitives injected into New.
type Config struct {
	// Ticket and Attempt identify the attempt this session belongs to. Both
	// required; together they are the worktree key and the session.json location.
	Ticket  string
	Attempt string

	// Adapter is the coding-agent name recorded in session.json (e.g.
	// "claude-code"). Descriptive only; the actual adapter comes from New's
	// factory.
	Adapter string

	// Model is an optional model id, recorded in session.json and used as the
	// spec's Model when Spec.Model is empty.
	Model string

	// Spec is the base session spec. Its WorkDir is always overwritten with the
	// attempt's worktree path, so any value set here is ignored.
	Spec agent.SessionSpec

	// Ref is the commit-ish the attempt's worktree branch starts from on first
	// creation. Empty means HEAD; ignored once the branch exists (a resume
	// re-attaches to it).
	Ref string
}

// Handle is one attempt's live-session controller. Construct it with New, then
// drive the session with Spawn / Kill / Resume. A Handle is safe for concurrent
// use; a typical caller ranges Stream() in one goroutine while another calls
// Prompt / Kill.
type Handle struct {
	sess       *session.Store
	wt         *worktree.Manager
	newAdapter func() agent.Adapter
	cfg        Config
	now        func() time.Time

	mu      sync.Mutex
	adapter agent.Adapter // the live process; nil before Spawn and after Kill
}

// New returns a Handle for cfg's attempt. sess persists the session identity, wt
// provides the isolating worktree, and newAdapter mints a fresh adapter for each
// Spawn/Resume (an adapter value is single-use, so one is needed per process).
// All three, plus cfg.Ticket and cfg.Attempt, are required.
func New(sess *session.Store, wt *worktree.Manager, newAdapter func() agent.Adapter, cfg Config) (*Handle, error) {
	switch {
	case sess == nil:
		return nil, errors.New("manage: session store is required")
	case wt == nil:
		return nil, errors.New("manage: worktree manager is required")
	case newAdapter == nil:
		return nil, errors.New("manage: adapter factory is required")
	case cfg.Ticket == "" || cfg.Attempt == "":
		return nil, errors.New("manage: ticket and attempt are required")
	}
	return &Handle{sess: sess, wt: wt, newAdapter: newAdapter, cfg: cfg, now: time.Now}, nil
}

func (h *Handle) key() worktree.Key {
	return worktree.Key{Ticket: h.cfg.Ticket, Attempt: h.cfg.Attempt}
}

// spec returns the base spec with WorkDir and Model resolved for this session.
func (h *Handle) spec(workdir string) agent.SessionSpec {
	s := h.cfg.Spec
	s.WorkDir = workdir
	if s.Model == "" {
		s.Model = h.cfg.Model
	}
	return s
}

// Spawn creates the attempt's worktree, starts a fresh agent session, and
// persists its identity to session.json. It errors if a session is already live.
// If the agent mints a session id but then fails to come up, that cattle handle
// is still persisted so the attempt stays inspectable and resumable.
func (h *Handle) Spawn(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.adapter != nil {
		return errors.New("manage: session already live")
	}

	wt, err := h.wt.Create(ctx, worktree.Spec{Key: h.key(), Ref: h.cfg.Ref})
	if err != nil {
		return fmt.Errorf("manage: worktree for %s/%s: %w", h.cfg.Ticket, h.cfg.Attempt, err)
	}

	a := h.newAdapter()
	id, spawnErr := a.Spawn(ctx, h.spec(wt.Path))
	ident := session.Identity{
		Adapter:   h.cfg.Adapter,
		Model:     h.cfg.Model,
		SessionID: id,
		PID:       pidOf(a),
		Worktree:  wt.Path,
		Started:   h.now(),
	}
	if spawnErr != nil {
		// The id is allocated before the process is confirmed, so persist whatever
		// handle we got — a failed spawn is still a resumable/inspectable session.
		if id != "" {
			_ = h.sess.WriteIdentity(ident)
		}
		return fmt.Errorf("manage: spawn %s/%s: %w", h.cfg.Ticket, h.cfg.Attempt, spawnErr)
	}
	if err := h.sess.WriteIdentity(ident); err != nil {
		// We can no longer track this process durably; don't leak it.
		_ = a.Kill()
		return fmt.Errorf("manage: persist identity for %s/%s: %w", h.cfg.Ticket, h.cfg.Attempt, err)
	}
	h.adapter = a
	return nil
}

// Kill reaps the live session process, keeping its session id and the ticket log
// for a later Resume — the whole point of the cattle handle. Only the now-stale
// pid in session.json is cleared. Kill is idempotent: with no live session it is
// a no-op, because the durable state that matters already survives on disk.
func (h *Handle) Kill() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.adapter == nil {
		return nil
	}
	if err := h.adapter.Kill(); err != nil {
		return fmt.Errorf("manage: kill %s/%s: %w", h.cfg.Ticket, h.cfg.Attempt, err)
	}
	h.adapter = nil
	// Clear only the pid; the session id, worktree, and log survive. A failed
	// rewrite can't resurrect the reaped process, so it must not fail the reap.
	if ident, err := h.sess.ReadIdentity(); err == nil {
		ident.PID = 0
		_ = h.sess.WriteIdentity(ident)
	}
	return nil
}

// Resume reloads the session id from session.json and spins a fresh process with
// the agent's --resume, re-attached to the same worktree. It keeps the session
// id, worktree, and birth time and only refreshes the pid — this is the same
// session in a new process, not a new session.
//
// Resume requires no session to be live; the reconcile loop's state machine
// reaps (Kill) before it respawns. After Resume the live process is new, so a
// caller must re-read Stream() to re-attach its watcher.
func (h *Handle) Resume(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.adapter != nil {
		return errors.New("manage: session already live; kill before resume")
	}

	ident, err := h.sess.ReadIdentity()
	if err != nil {
		return fmt.Errorf("manage: resume %s/%s: %w", h.cfg.Ticket, h.cfg.Attempt, err)
	}
	if ident.SessionID == "" {
		return fmt.Errorf("manage: resume %s/%s: no session id on record", h.cfg.Ticket, h.cfg.Attempt)
	}

	// Create is idempotent and crash-safe: it returns the existing worktree, or
	// re-attaches to the surviving branch if a restart's Reconcile swept the
	// checkout out from under a stopped session.
	wt, err := h.wt.Create(ctx, worktree.Spec{Key: h.key(), Ref: h.cfg.Ref})
	if err != nil {
		return fmt.Errorf("manage: worktree for %s/%s: %w", h.cfg.Ticket, h.cfg.Attempt, err)
	}

	a := h.newAdapter()
	if err := a.Resume(ctx, ident.SessionID, h.spec(wt.Path)); err != nil {
		return fmt.Errorf("manage: resume %s/%s: %w", h.cfg.Ticket, h.cfg.Attempt, err)
	}
	ident.PID = pidOf(a)
	ident.Worktree = wt.Path
	if err := h.sess.WriteIdentity(ident); err != nil {
		_ = a.Kill()
		return fmt.Errorf("manage: persist identity for %s/%s: %w", h.cfg.Ticket, h.cfg.Attempt, err)
	}
	h.adapter = a
	return nil
}

// Stream returns the live session's normalized event channel, or nil if no
// session is live. Because each Spawn/Resume starts a fresh process, the channel
// differs across them — re-read Stream() after every Spawn or Resume to re-attach
// a consumer (typically watch.Watcher).
func (h *Handle) Stream() <-chan agent.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.adapter == nil {
		return nil
	}
	return h.adapter.Stream()
}

// Online exposes the live adapter's online signal — a channel closed once the
// session has come online — when the adapter implements agent.Onliner, else nil.
// A nil channel means the adapter cannot confirm coming online, so a caller
// treats the session as online (the pre-cascade behaviour). It is the seam the
// reconcile loop's confirm-on-resume uses to tell a live resume from one that
// died on a stale session id (drvctl-016).
func (h *Handle) Online() <-chan struct{} {
	a := h.live()
	if a == nil {
		return nil
	}
	o, ok := a.(agent.Onliner)
	if !ok {
		return nil
	}
	return o.Online()
}

// Prompt submits a user turn to the live session (the reconcile loop uses it to
// inject the cold-start brief). It errors if no session is live.
func (h *Handle) Prompt(ctx context.Context, text string) error {
	a := h.live()
	if a == nil {
		return errors.New("manage: no live session to prompt")
	}
	return a.Prompt(ctx, text)
}

// Interrupt cancels the live session's in-flight turn without ending the session.
// It errors if no session is live.
func (h *Handle) Interrupt(ctx context.Context) error {
	a := h.live()
	if a == nil {
		return errors.New("manage: no live session to interrupt")
	}
	return a.Interrupt(ctx)
}

// Decide answers a pending tool-permission request back to the live session's
// adapter, correlating by request id. It is the seam the reconcile loop's two
// gates (internal/gate and internal/protocol, which both hold a Decider) use to
// allow, withhold, or deny a call — the live adapter is the only thing that can
// reach the agent's permission callback, and manage owns the adapter, so it
// exposes this passthrough rather than leaking the adapter out.
//
// It errors if no session is live, or if the live adapter's agent has no
// permission callback (it does not implement agent.Permissioner — e.g. one that
// pre-authorizes tools via flags), in which case there is nothing to answer.
func (h *Handle) Decide(ctx context.Context, requestID string, d agent.Decision) error {
	a := h.live()
	if a == nil {
		return errors.New("manage: no live session to decide")
	}
	p, ok := a.(agent.Permissioner)
	if !ok {
		return errors.New("manage: live adapter has no permission callback")
	}
	return p.Decide(ctx, requestID, d)
}

// Identity returns the persisted session identity from session.json. It is
// available even after Kill — reading the surviving cattle handle is the point.
func (h *Handle) Identity() (session.Identity, error) {
	return h.sess.ReadIdentity()
}

// Live reports whether a session process is currently attached to this Handle.
func (h *Handle) Live() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.adapter != nil
}

// live returns the current adapter under the lock, so a caller can act on it
// after releasing the lock without racing a concurrent Kill's nil-out.
func (h *Handle) live() agent.Adapter {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.adapter
}

// pidOf reads the adapter's process id if it exposes one via the optional
// PID-accessor interface, else 0. pid is best-effort runtime metadata (for status
// and re-adoption), not part of the core agent.Adapter seam.
func pidOf(a agent.Adapter) int {
	if p, ok := a.(interface{ PID() int }); ok {
		return p.PID()
	}
	return 0
}
