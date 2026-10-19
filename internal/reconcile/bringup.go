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
	"errors"
	"fmt"
	"io/fs"
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

// resumeContextHeadroom is the fraction of the context-window limit a parked
// session's recorded fill must stay under for the resume fast-path to reuse it
// (drvctl-056). A conversation resumed already near the ceiling would trip the
// context auto-stop (internal/limit) on its first frame and strand the attempt,
// so above this fraction bringUp cold-starts a fresh session (a clean window)
// instead. It is a tunable heuristic, not a hard invariant; the limit gate
// remains the real backstop.
const resumeContextHeadroom = 0.8

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
	// forever"). A recorded id whose parked context is near the window limit is
	// deliberately *not* resumed either — resuming it would trip the context
	// auto-stop immediately, so bringUp cold-starts a fresh session with a clean
	// window and re-grounds it from the brief (drvctl-056).
	resuming := false
	if id, err := sess.ReadIdentity(); err == nil && id.SessionID != "" {
		resuming = true
	}
	if resuming && !r.resumeHasHeadroom(a.Ticket, a.ID) {
		r.opt.Logf("reconcile: resume %s/%s skipped: parked context near the window limit, cold-starting fresh", a.Ticket, a.ID)
		resuming = false
	}
	spawned := false
	if resuming {
		if err := handle.Resume(ctx); err != nil {
			// The recorded id could not even be launched; fall through to a fresh
			// spawn rather than failing the whole bring-up.
			r.opt.Logf("reconcile: resume %s/%s failed to launch, spawning fresh: %v", a.Ticket, a.ID, err)
			resuming = false
		} else {
			// Prime the first user turn *before* confirming online: headless
			// `claude --resume` emits no system/init frame until it receives a user
			// turn, so without this the confirm below could never observe the resume
			// come up and would always fall through to a cold-start (drvctl-056). The
			// nudge is the one driving turn the resumed process needs; drive() (which
			// otherwise injects it) now only briefs a fresh spawn. If the id is in fact
			// unresumable the confirm still times out and the primed turn dies with the
			// killed process — no double-prompt reaches the fresh spawn.
			if perr := protocol.InjectResumeNudge(ctx, a.Ticket, r.asyncWakeNote(a.Ticket, a.ID), handle); perr != nil {
				r.opt.Logf("reconcile: prime resume nudge %s/%s: %v", a.Ticket, a.ID, perr)
			}
			if confirmed, cerr := r.confirmOnline(ctx, handle); cerr != nil {
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
// already being consumed when the agent starts producing. A headless stream-json
// process produces nothing until it receives a user turn, so it must always get
// one — but which turn depends on the session identity (drvctl-022): a fresh spawn
// (empty context window) gets the full cold-start brief here; a resume (recorded
// id came back online, context intact) was already primed with its short
// continuation nudge inside bringUp — that priming is what brought the resumed
// process online (drvctl-056), so drive injects nothing for it. The brief
// injection failing does not tear the live session down — it is logged and the
// session works on.
func (r *Reconciler) drive(ctx context.Context, a project.Attempt, w *wired) {
	if !w.spawned {
		// Resume: the nudge was primed in bringUp before confirmOnline; re-injecting
		// here would double-prompt the session. Nothing to do.
		return
	}
	if err := protocol.InjectBrief(ctx, r.opt.Root, a.Ticket, a.ID, w.handle); err != nil {
		r.opt.Logf("reconcile: inject brief %s/%s: %v", a.Ticket, a.ID, err)
	}
}

// resumeHasHeadroom reports whether a recorded session's parked context fill
// leaves enough room under the context-window limit to be worth resuming
// (drvctl-056). A parked conversation already past resumeContextHeadroom of the
// hard limit would trip the context auto-stop on its first frame, so bringUp
// cold-starts a fresh session (a clean window) instead. With no limit configured
// there is nothing to crowd, and a session that never metered a frame has no
// recorded fill, so both are treated as resumable. A read error is also treated
// as headroom: the limit gate remains the real backstop, so a bad meter read
// should not by itself discard an otherwise resumable conversation.
func (r *Reconciler) resumeHasHeadroom(ticket, attempt string) bool {
	if r.opt.ContextLimit <= 0 {
		return true
	}
	sess, err := session.Open(r.opt.Root, ticket, attempt)
	if err != nil {
		r.opt.Logf("reconcile: resume headroom %s/%s: open session: %v", ticket, attempt, err)
		return true
	}
	defer sess.Close()
	m, err := sess.ReadMeter()
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.opt.Logf("reconcile: resume headroom %s/%s: read meter: %v", ticket, attempt, err)
		}
		return true // no meter (or unreadable) ⇒ nothing to crowd the window
	}
	ceiling := int(float64(r.opt.ContextLimit) * resumeContextHeadroom)
	return m.Usage.ContextTokens < ceiling
}

// asyncWakeNote returns the wake-reason line appended to a resume nudge when the
// attempt was just woken by its own detached test's result landing (drvctl-056):
// the resumed session kept the context in which it launched the test, so it only
// needs to be told the verdict is in — read it and claim review / iterate —
// rather than cold-starting and re-deriving its just-done state with a recall
// round-trip (the drvctl-055 cold-start-on-wake this amends). It reloads the
// attempt fresh because the `woken-by` activation is authored during desired() on
// the same tick, so the attempt handed to admit/bringUp predates it. Empty when
// this resume is not an async wake (the plain continuation nudge is used) or the
// attempt cannot be reloaded.
func (r *Reconciler) asyncWakeNote(ticket, attempt string) string {
	a, err := project.LoadAttempt(r.opt.Root, ticket, attempt)
	if err != nil {
		return ""
	}
	var lastPark, lastWoken, resultSeq int
	for _, e := range a.Events {
		switch e.Type {
		case "park":
			if e.Seq > lastPark {
				lastPark = e.Seq
			}
		case wokenByType:
			// A woken-by naming this attempt's OWN ticket is an async wake; WokenSeq is
			// the test-result it answered (mirrors reconcileAsyncWorker's reading).
			if e.WokenChild == a.Ticket && e.WokenSeq > 0 && e.Seq > lastWoken {
				lastWoken = e.Seq
				resultSeq = e.WokenSeq
			}
		}
	}
	// Only a wake *after* the latest park is live: an older woken-by was already
	// handled in a prior session.
	if lastWoken <= lastPark || resultSeq == 0 {
		return ""
	}
	return fmt.Sprintf("You were woken because the detached `draiver test` you launched has landed its verdict: `test-result` #%d is now on your log. Read it with `draiver recall %s %d`, then claim `review` if it is green or iterate if it is red — you still have the context from when you launched it, so no need to rederive your just-done work.", resultSeq, ticket, resultSeq)
}
