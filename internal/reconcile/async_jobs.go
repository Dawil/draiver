// This file holds drvctl-055's worker-side async-job park/wake pass — the leaf
// analog of the coordinator machinery in coordinator_gate.go (dormancy trim) +
// reverse_wants.go (activation). The two are folded into one pass here because a
// worker's async job lives entirely on its *own* log: there is no cross-ticket
// `wants:` edge, so the doorbell (a `test-result`) and the activation (`woken-by`)
// sit on the same attempt. It is what lets `park` generalise past the manager — a
// plain leaf worker does `draiver test --detach && draiver park`, suspends to
// Pending while the detached climb runs, and is woken by the result landing on its
// own log (docs/manager-as-worker.md §"park as a general primitive").
//
// The pattern on a worker's own log, in order:
//
//	async-job #N     launch marker — the detached test started (wake.AsyncJob source)
//	park      #M     the worker suspended behind it (M > N)
//	test-result #R   the detached runner landed a pass/fail verdict (R > M)
//	woken-by  #A     THIS pass authored it (WokenChild = own ticket, WokenSeq = R)
//
// Serial by construction: launch-one, park, wake on the result, so at most one
// result is unhandled at a time. The pass drives this as a phase machine over the
// desired set `out`, exactly mirroring how the coordinator gate trims and
// reverse-`wants:` re-adds — a worker stays Enabled throughout, so trimming it from
// `out` (not admitting) is what projects it as Pending (project.Control), never a
// disable.
package reconcile

import (
	"fmt"
	"sort"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// asyncJobType and testResultType are the two log event types this pass keys on —
// the detached-test launch marker and the result that answers it (drvctl-055). Kept
// as local literals matching the package convention (there is no central registry);
// they mirror the same strings in internal/wake and cmd. wokenByType is shared with
// reverse_wants.go — the same spine-authored activation event, here naming the
// worker's own ticket as the "child" whose doorbell woke it.
const (
	asyncJobType   = "async-job"
	testResultType = "test-result"
)

// reconcileAsyncJobs runs the per-worker async park/wake phase machine over every
// attempt that has launched a detached test. It runs last in desired() (after the
// coordinator passes), additive like activateReverseWants: it only ever trims a
// worker it is suspending or (re-)adds one it is waking, so it cannot regress the
// direct/wants/gate passes for a non-async attempt (which it skips entirely).
func (r *Reconciler) reconcileAsyncJobs(all []project.Attempt, out map[worktree.Key]project.Attempt) error {
	// Latest attempt per ticket (LoadAll is sorted by attempt id, so the last entry
	// wins) — the async job is a property of the attempt currently being supervised.
	latestByTicket := make(map[string]project.Attempt, len(all))
	for _, a := range all {
		latestByTicket[a.Ticket] = a
	}

	// A stable order so any authored `woken-by` is deterministic across ticks.
	tickets := make([]string, 0, len(latestByTicket))
	for t := range latestByTicket {
		tickets = append(tickets, t)
	}
	sort.Strings(tickets)

	live := r.liveSet()
	for _, t := range tickets {
		if err := r.reconcileAsyncWorker(latestByTicket[t], live, out); err != nil {
			return err
		}
	}
	return nil
}

// reconcileAsyncWorker drives one attempt's async phase machine. It is a no-op
// unless the attempt is an enabled, Running worker that has launched a detached
// test — a human `disable` is a "leave it alone" signal, and a non-Running worker
// is either blocked on its own escalation (Needs-me ⇒ doorbells wait until the
// resolution returns it to Running) or already Review/Done (nothing to wake). The
// Running guard is the same suppress-while-blocked seam reverse_wants.go leans on.
func (r *Reconciler) reconcileAsyncWorker(w project.Attempt, live map[worktree.Key]bool, out map[worktree.Key]project.Attempt) error {
	if !w.Enabled || w.State != project.Running {
		return nil
	}

	// Phase from the worker's own log: the latest launch, park and activation, plus
	// which results a prior `woken-by` already handled.
	var lastLaunchSeq, lastParkSeq, lastActivationSeq int
	handledResult := map[int]bool{}
	for _, e := range w.Events {
		switch e.Type {
		case asyncJobType:
			if e.Seq > lastLaunchSeq {
				lastLaunchSeq = e.Seq
			}
		case "park":
			if e.Seq > lastParkSeq {
				lastParkSeq = e.Seq
			}
		case wokenByType:
			if e.Seq > lastActivationSeq {
				lastActivationSeq = e.Seq
			}
			// A woken-by naming this attempt's OWN ticket is an async activation; its
			// WokenSeq is the result it answered. (A woken-by naming a different ticket is
			// this attempt acting as a coordinator — not ours to read here.)
			if e.WokenChild == w.Ticket && e.WokenSeq > 0 {
				handledResult[e.WokenSeq] = true
			}
		}
	}

	// Never launched a detached test ⇒ not an async worker; leave it entirely to the
	// direct/wants/gate passes (a plainly-working leaf must stay desired).
	if lastLaunchSeq == 0 {
		return nil
	}

	key := worktree.Key{Ticket: w.Ticket, Attempt: w.ID}

	// active: an activation later than the latest park ⇒ a result is in flight being
	// handled by a (re)admitted session. Keep it desired so retire never reaps it
	// mid-handling; write nothing — one wake, one result.
	if lastActivationSeq > lastParkSeq {
		out[key] = w
		return nil
	}

	// Otherwise dormant. Is there a result waiting to be handled?
	resultSeq := oldestUnhandledAsyncResult(w.Events, handledResult)

	if resultSeq == 0 {
		// No result yet. Suspend only a worker that has actually PARKED behind its
		// latest launch — trimming it from `out` lets retire reap the idle session to
		// Pending (worktree kept warm, the climb runs in it). A worker that launched but
		// has not parked yet is still mid-turn and live; leave it desired so we never
		// reap a session that is still running.
		if lastParkSeq > lastLaunchSeq {
			delete(out, key)
		}
		return nil
	}

	// A result is waiting. If the launch-turn session is still winding down, let
	// retire reap it first (trim); a later tick admits the result once reaped — the
	// same live-vs-reaped discipline reverse_wants.go uses.
	if live[key] {
		delete(out, key)
		return nil
	}

	// Reaped, with a result waiting: wake it. Author the `woken-by` activation on the
	// worker's own log (WokenChild = own ticket, reusing drvctl-051's one-entry record
	// — dedup marker, replay order, and the cold-start brief's wake-reason tail) and
	// add it back to the desired set so the next tick admits it to claim review
	// (green) or iterate (red).
	if _, err := ticketlog.Append(r.opt.Root, w.Ticket, w.ID, event.Event{
		Type:       wokenByType,
		Actor:      r.opt.Actor,
		WokenChild: w.Ticket,
		WokenSeq:   resultSeq,
		Body:       wokenByAsyncBody(resultSeq),
	}); err != nil {
		return fmt.Errorf("reconcile: author async %s on %s/%s: %w", wokenByType, w.Ticket, w.ID, err)
	}
	out[key] = w
	return nil
}

// oldestUnhandledAsyncResult returns the seq of the earliest `test-result` that
// answers a detached launch and has no matching `woken-by` yet, or 0 if none. It
// walks the log in seq order tracking an open launch window: only a result that
// closes an `async-job` launch counts, so a synchronous `test --log` result (which
// has no preceding open launch) is never mistaken for an async doorbell. Serial by
// construction, so in practice there is at most one.
func oldestUnhandledAsyncResult(events []event.Event, handled map[int]bool) int {
	ordered := append([]event.Event(nil), events...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })

	launchOpen := false
	for _, e := range ordered {
		switch e.Type {
		case asyncJobType:
			launchOpen = true
		case testResultType:
			if !launchOpen {
				continue // a sync `test --log` result, not an async doorbell.
			}
			launchOpen = false // this result answers the open launch.
			if !handled[e.Seq] {
				return e.Seq
			}
		}
	}
	return 0
}

// wokenByAsyncBody renders the wake-reason recorded on the async activation event —
// the tail of the woken worker's cold-start brief. The board renders event bodies as
// Markdown, so the result reference is backticked.
func wokenByAsyncBody(resultSeq int) string {
	return fmt.Sprintf("Woken by own `test-result` #%d — the detached `draiver test` this attempt launched has landed its verdict on this log. Authored by the reconciler at wake time: the dedup marker for this result and the wake-reason tail of this brief. Cold-start, read the result, and claim review (green) or iterate (red).", resultSeq)
}
