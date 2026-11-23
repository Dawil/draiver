// This file holds the reconcile side of the checkpoint-before-reap (drvctl-061):
// the auto-stop outcome reporter the limit gate calls back into, and the `ctl stop`
// handoff — a client writes a nonce-stamped stop request, the running `ctl up` that
// owns the live session runs the checkpoint and reaps, and writes a result the
// client waits on. The checkpoint itself lives in internal/handoff.
package reconcile

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Dawil/draiver/internal/handoff"
	"github.com/Dawil/draiver/internal/limit"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/systemlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// stopServeSlack is how long, beyond the grace period, a `ctl stop` client waits for
// the daemon to pick up and answer its request (a tick or two) before it falls back
// to a direct reap.
const stopServeSlack = 30 * time.Second

// defaultStopPoll is how often a waiting `ctl stop` client re-reads the result.
const defaultStopPoll = 200 * time.Millisecond

// reportAutoStop records how an auto-stop checkpoint ended as a system event (and in
// the daemon's operator log): note written or timed out, and whether the supervisor
// restarted the attempt itself.
func (r *Reconciler) reportAutoStop(key worktree.Key, rep limit.Report) {
	msg := fmt.Sprintf("ctl: context auto-stop (escalation #%d) — %s", rep.Escalation, rep.Result.Describe())
	switch {
	case rep.Restarted:
		msg += fmt.Sprintf("; resolved by the supervisor (#%d), restarting on a fresh session (auto-restart %d of %d)",
			rep.Resolution, rep.Restarts+1, r.opt.ContextAutoRestarts)
	case rep.Result.Outcome == handoff.Written:
		msg += fmt.Sprintf("; auto-restart budget spent (%d of %d) — left for a human", rep.Restarts, r.opt.ContextAutoRestarts)
	default:
		msg += "; left for a human"
	}
	if rep.HaltErr != nil {
		msg += fmt.Sprintf("; reap failed: %v", rep.HaltErr)
	}
	r.emitSystem(key, systemlog.SystemEvent{
		Class:   systemlog.ClassCtlSession,
		Message: msg,
		Fields: map[string]string{
			"handoff":    string(rep.Result.Outcome),
			"escalation": fmt.Sprint(rep.Escalation),
		},
	})
	r.opt.Logf("reconcile: %s/%s: %s", key.Ticket, key.Attempt, msg)
}

// HeadroomWarning is handoff.HeadroomWarning, re-exported for the daemon's start-up
// check.
func HeadroomWarning(contextLimit, window int) string {
	return handoff.HeadroomWarning(contextLimit, window)
}

// stopRequest is the `ctl stop` handoff a client hands the running daemon.
type stopRequest struct {
	ID    string    `json:"id"`    // correlates the daemon's result with this request
	Nonce string    `json:"nonce"` // the controller the client handed it to
	Stamp time.Time `json:"stamp"`
}

// stopResult is how the daemon served a stop request.
type stopResult struct {
	ID       string `json:"id"`
	PID      int    `json:"pid,omitempty"`
	Signaled bool   `json:"signaled,omitempty"`
	Handoff  bool   `json:"handoff,omitempty"`  // the checkpoint ran
	Outcome  string `json:"outcome,omitempty"`  // handoff.Outcome when Handoff
	NoteSeq  int    `json:"note_seq,omitempty"` // the handoff note, when written
	Direct   bool   `json:"direct,omitempty"`   // reaped with no checkpoint
	Note     string `json:"note,omitempty"`     // why, for the client to print
	Err      string `json:"err,omitempty"`
}

// StopWithHandoff is `ctl stop`'s default (drvctl-061): when a running `ctl up`
// owns the session it hands the stop to it, so the owning process asks the session
// for a handoff note before reaping it; it then waits for the daemon's result. With
// now (`ctl stop --now`), a zero grace period, or no running `ctl up`, it reaps
// directly exactly as Stop always has — and says so in StopResult.Note. A daemon
// that does not answer within the grace period plus a little slack is treated the
// same way: the client falls back to the direct reap.
func (r *Reconciler) StopWithHandoff(ticket, attempt string, now bool) (StopResult, error) {
	if now || r.opt.StopGrace <= 0 {
		return r.Stop(ticket, attempt)
	}
	ctrl, ok := r.liveController()
	if !ok {
		res, err := r.Stop(ticket, attempt)
		res.Direct = true
		res.Note = "no running `ctl up` owns the session, so it was reaped directly with no handoff note"
		return res, err
	}
	id, err := NewNonce()
	if err != nil {
		return StopResult{}, err
	}
	resultPath := r.opt.Root.StopResultPath(ticket, attempt)
	_ = os.Remove(resultPath)
	if err := writeJSONAtomic(r.opt.Root.StopRequestPath(ticket, attempt), stopRequest{ID: id, Nonce: ctrl.Nonce, Stamp: r.opt.Now()}); err != nil {
		return StopResult{}, fmt.Errorf("reconcile: hand stop to ctl up: %w", err)
	}

	poll := r.opt.HandoffPoll
	if poll <= 0 {
		poll = defaultStopPoll
	}
	deadline := time.Now().Add(r.opt.StopGrace + stopServeSlack)
	for time.Now().Before(deadline) {
		var got stopResult
		if ok, err := readJSON(resultPath, &got); err == nil && ok && got.ID == id {
			_ = os.Remove(resultPath)
			res := StopResult{PID: got.PID, Signaled: got.Signaled, AlreadyStopped: !got.Signaled,
				Handoff: got.Handoff, Outcome: got.Outcome, NoteSeq: got.NoteSeq, Direct: got.Direct,
				Note: got.Note, ControllerPID: ctrl.PID}
			if got.Err != "" {
				return res, fmt.Errorf("reconcile: ctl up stop %s/%s: %s", ticket, attempt, got.Err)
			}
			return res, nil
		}
		time.Sleep(poll)
	}
	_ = os.Remove(r.opt.Root.StopRequestPath(ticket, attempt))
	res, err := r.Stop(ticket, attempt)
	res.Direct = true
	res.Note = fmt.Sprintf("`ctl up` (pid %d) did not answer within %s, so the session was reaped directly with no handoff note",
		ctrl.PID, (r.opt.StopGrace + stopServeSlack).Round(time.Second))
	return res, err
}

// serveStopRequests answers every pending `ctl stop` handoff (drvctl-061). A request
// stamped by another controller (a previous boot) is swept unanswered — its client
// times out and falls back to a direct reap. Errors are logged, never fatal to the
// tick.
func (r *Reconciler) serveStopRequests(ctx context.Context) {
	tickets, err := r.opt.Root.ListTickets()
	if err != nil {
		return
	}
	ctrl, live := r.liveController()
	for _, t := range tickets {
		atts, err := r.opt.Root.ListAttempts(t)
		if err != nil {
			continue
		}
		for _, att := range atts {
			path := r.opt.Root.StopRequestPath(t, att)
			var req stopRequest
			ok, err := readJSON(path, &req)
			if err != nil {
				r.opt.Logf("reconcile: read stop request %s/%s: %v", t, att, err)
				_ = os.Remove(path)
				continue
			}
			if !ok {
				continue
			}
			_ = os.Remove(path) // claim it
			if !live || req.Nonce != ctrl.Nonce {
				continue
			}
			r.serveStop(ctx, worktree.Key{Ticket: t, Attempt: att}, req)
		}
	}
}

// serveStop serves one `ctl stop` request. For a live session this daemon owns, it
// records the stop's hold first (the interrupt hold + marker sweep, so the reaped
// run is never re-admitted), then runs the checkpoint and reaps in the background,
// writing the result when done. Anything else — no run, an adopted foreign process,
// an already-spent session — is reaped directly, and the result says why.
func (r *Reconciler) serveStop(ctx context.Context, key worktree.Key, req stopRequest) {
	r.mu.Lock()
	rn := r.runs[key]
	r.mu.Unlock()

	if rn != nil && !rn.adopted && !rn.spent() && rn.limitGate != nil && rn.limitGate.Checkpointing() {
		// A context auto-stop handoff is already running and will reap the session;
		// just make the stop stick.
		err := r.stopHold(key.Ticket, key.Attempt)
		res := stopResult{ID: req.ID, Handoff: true, Note: "a context auto-stop handoff was already in progress; it reaps the session when done"}
		if err != nil {
			res.Err = err.Error()
		}
		r.writeStopResult(key, res)
		return
	}
	if rn == nil || rn.adopted || rn.spent() || rn.stopCheckpoint() != nil {
		why := "`ctl up` does not own a live session for it"
		if rn != nil && rn.adopted {
			why = "the session is a re-adopted process `ctl up` cannot talk to"
		}
		res, err := r.Stop(key.Ticket, key.Attempt)
		out := stopResult{ID: req.ID, PID: res.PID, Signaled: res.Signaled, Direct: true,
			Note: why + ", so it was reaped directly with no handoff note"}
		if err != nil {
			out.Err = err.Error()
		}
		r.writeStopResult(key, out)
		return
	}

	if err := r.stopHold(key.Ticket, key.Attempt); err != nil {
		r.writeStopResult(key, stopResult{ID: req.ID, Err: err.Error()})
		return
	}
	pid := 0
	if id, ok := r.readIdentity(key); ok {
		pid = id.PID
	}
	ck := handoff.New(r.opt.Root, key.Ticket, key.Attempt, rn.handle, r.opt.StopGrace)
	if r.opt.HandoffPoll > 0 {
		ck.Poll = r.opt.HandoffPoll
	}
	rn.stopMu.Lock()
	rn.stopCk = ck
	rn.stopMu.Unlock()
	inFlight := rn.turnActive.Load()
	go func() {
		res := ck.Run(ctx, inFlight, "a human stopped it with `ctl stop`")
		killErr := rn.handle.Kill()
		rn.stopMu.Lock()
		rn.stopCk = nil
		rn.stopMu.Unlock()

		msg := "ctl: stopped by human — " + res.Describe()
		out := stopResult{ID: req.ID, PID: pid, Signaled: killErr == nil && pid != 0, Handoff: true,
			Outcome: string(res.Outcome), NoteSeq: res.NoteSeq}
		if killErr != nil {
			out.Err = killErr.Error()
			msg += fmt.Sprintf("; reap failed: %v", killErr)
		}
		r.emitSystem(key, systemlog.SystemEvent{Class: systemlog.ClassCtlSession, Message: msg,
			Fields: map[string]string{"handoff": string(res.Outcome)}})
		r.writeStopResult(key, out)
	}()
}

func (r *Reconciler) writeStopResult(key worktree.Key, res stopResult) {
	if err := writeJSONAtomic(r.opt.Root.StopResultPath(key.Ticket, key.Attempt), res); err != nil {
		r.opt.Logf("reconcile: write stop result %s/%s: %v", key.Ticket, key.Attempt, err)
	}
}

// stopHold is Stop's bookkeeping without the reap: a human stopping an enabled,
// Running attempt is an interrupt hold (drvctl-060), and any desired-marker is
// swept, so the attempt stays stopped once it is reaped.
func (r *Reconciler) stopHold(ticket, attempt string) error {
	if a, lerr := project.LoadAttempt(r.opt.Root, ticket, attempt); lerr == nil && a.Enabled && a.State == project.Running {
		r.recordInterrupt(worktree.Key{Ticket: ticket, Attempt: attempt}, "the human stopped the session with `ctl stop`")
	}
	if err := removeDesiredMarker(r.opt.Root, ticket, attempt); err != nil {
		return fmt.Errorf("reconcile: clear desired marker %s/%s: %w", ticket, attempt, err)
	}
	return nil
}
