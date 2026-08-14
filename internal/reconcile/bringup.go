// This file holds draiverctld's bring-up cascade — the piece of the reconcile
// loop that turns a desired attempt into a live, driven session. admit (in
// reconcile.go) owns the loop-side bookkeeping; everything here is the "how does
// a session get brought up and driven" story, concentrated so it can be reviewed
// on its own:
//
//   - bringUp climbs the self-heal cascade (resume the recorded session, confirm
//     it came online, else fall through to a fresh spawn), wires the session to
//     its gates + watcher, and returns a wired ready for admit to consume.
//   - confirmOnline bounds the wait for a Resumed session to signal online.
//   - drive gives the just-brought-up session its first user turn: the full
//     cold-start brief on a fresh spawn, a short nudge on a resume — the
//     brief-vs-resume decision where the drvctl-022 regression hid.
package reconcile

import (
	"context"
	"fmt"
	"time"

	"github.com/Dawil/draiver/internal/completion"
	"github.com/Dawil/draiver/internal/gate"
	"github.com/Dawil/draiver/internal/limit"
	"github.com/Dawil/draiver/internal/manage"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/protocol"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/watch"
)

// defaultAdapter is the adapter name assumed for an attempt whose attempt.md
// records none — Claude Code is the first and reference adapter (Tier 0).
const defaultAdapter = "claude-code"

// wired is a brought-up live session with the ingest machinery bound to its
// stream: the handle owning the process, the session store, and the two gates
// plus the watcher each of its events is dispatched through. admit obtains one
// from bringUp — the single place the session and its watch/gate/protocol
// pipeline are wired. Since the imperative verbs became daemon handoffs
// (drvctl-016), admit is the only caller, so the client path cannot drift from
// it: `start`/`restart` write a marker and the daemon does the bring-up.
type wired struct {
	handle       *manage.Handle
	sess         *session.Store
	protoGate    *protocol.Gate
	permGate     *gate.Gate
	limitGate    *limit.Gate
	completeGate *completion.Gate
	watcher      *watch.Watcher
	repo         string // resolved repo path, recorded on the run for retire

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
// caller (admit) injects the cold-start brief once it is ready to consume the
// stream — but only when bringUp spawned fresh (wired.spawned), the
// brief-on-reset coupling.
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
		// Pin the adapter version resolved once at daemon start into every session's
		// provenance (drvctl-033); empty when the version probe couldn't run.
		AdapterVersion: r.opt.AdapterVersion,
		Model:          a.Model,
		// Cut the attempt's branch from its recorded base so a fresh land is a clean
		// fast-forward (base is an ancestor by construction). Empty for a legacy
		// attempt that recorded no base, which falls back to HEAD as before
		// (drvctl-021).
		Ref:  a.Base,
		Spec: r.opt.BaseSpec,
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

	// The completion gate's baseline is the same log-tail-at-session-start snapshot
	// the protocol gate takes: a review/escalation must be logged *during* this
	// session to count as its hand-off, so snapshot it now, before the session works.
	completeGate, err := completion.New(r.opt.Root, a.Ticket, a.ID, r.opt.Actor, handle.Prompt, handle.Kill)
	if err != nil {
		_ = handle.Kill()
		_ = sess.Close()
		return nil, fmt.Errorf("completion gate: %w", err)
	}
	watcher := watch.New(r.opt.Root, a.Ticket, a.ID, r.opt.Actor, sess, watch.ProtocolRecognizer{Ticket: a.Ticket})

	return &wired{handle: handle, sess: sess, protoGate: protoGate, permGate: permGate, limitGate: limitGate, completeGate: completeGate, watcher: watcher, repo: repo, spawned: spawned}, nil
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

// drive gives a just-brought-up session its first user turn, so the stream is
// already being consumed when the agent starts producing. A headless
// stream-json process produces nothing until it receives a user turn, so it must
// always get one — but which turn depends on the session identity (drvctl-022): a
// fresh spawn (empty context window) gets the full cold-start brief; a resume
// (recorded id came back online, context intact) gets a short nudge to continue
// rather than the whole brief re-dumped (the brief-on-reset coupling's legitimate
// intent, drvctl-016). Either injection failing does not tear the live session
// down — it is logged and the session works on.
func (r *Reconciler) drive(ctx context.Context, a project.Attempt, w *wired) {
	if w.spawned {
		if err := protocol.InjectBrief(ctx, r.opt.Root, a.Ticket, a.ID, w.handle); err != nil {
			r.opt.Logf("reconcile: inject brief %s/%s: %v", a.Ticket, a.ID, err)
		}
	} else if err := protocol.InjectResumeNudge(ctx, a.Ticket, w.handle); err != nil {
		r.opt.Logf("reconcile: inject resume nudge %s/%s: %v", a.Ticket, a.ID, err)
	}
}
