// This file holds generalised reverse-`wants:` activation — the reconcile loop's
// second scheduler increment (drvctl-041), widened by drvctl-051 from the
// escalation-only `OnFailure=` analog into the manager's full wake machinery
// (docs/manager-as-worker.md §"The lifecycle"). It still wakes a dormant
// (Pending) **Capability** that `wants:` a child, but along two widened axes and
// with a spine-authored dedup/record event:
//
//   - more event types — a doorbell is any child-log event of a *trigger type*
//     (escalation | review | done, see doorbellTypes), not only an open
//     escalation. Edge-triggered: one event = one doorbell, so a child *sitting*
//     in Review/Done never re-wakes the manager.
//   - the `woken-by` activation event — at the moment the spine decides to wake a
//     manager it appends a durable `woken-by <child> #<seq>` event on the
//     **manager's own log**, before the agent runs. One entry serves three
//     consumers: dedup (a handled doorbell has a matching activation; an unhandled
//     one does not), replay ordering (timestamped), and the cold-start brief's
//     wake-reason tail (drvctl-053 renders it). The reconciler authoring a durable
//     event is the one mildly new thing — sound here because only the spine knows
//     the wake happened and the decision is deterministic.
//
// The **queue** is derived, never materialised: child doorbells with no matching
// activation event. The replay is **one doorbell per wake, oldest-first**: the
// spine takes the oldest unhandled doorbell, writes its activation, wakes the
// manager; the manager handles it and `park`s; the spine takes the next. Serial by
// design (docs/manager-as-worker.md).
//
//	wake iff  enabled ∧ pre-digest
//	          ∧ (∃ child doorbell with no activation event)   // queue non-empty
//	          ∧ (the manager has no open escalation of its own) // not blocked
//
// The last clause (**suppress while blocked**) falls out of the existing Running
// guard: a manager with its own open escalation derives Needs-me, so it is not
// Running and is skipped — child doorbells accumulate unhandled. When the human
// resolves it, it returns to Running and the replay resumes at the oldest
// still-unhandled doorbell.
//
// It is an **additive** contributor to `desired()` (parallel to propagateWants),
// so it can only *add* a wake and never regress the direct/wants/gate passes.
package reconcile

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// wokenByType is the event type the reconciler appends to a coordinator's own log
// at wake time — the spine-authored activation event (drvctl-051). It is not a
// lifecycle type (it never moves the control state) and not a doorbell type (it
// lives on the parent, not a child), so it only ever reads back as a dedup marker
// and the brief's wake-reason tail.
const wokenByType = "woken-by"

// doorbellTypes is the trigger set: the child-log event types that constitute a
// doorbell (acceptance #1 enumerates Escalated/Review/Done). Kept as one named set
// so narrowing the trigger set (the spec's "decide at build" note permits shipping
// Escalate+Done only) is a one-line change here; the wake mechanism is uniform
// across all three and edge-triggering makes a child *sitting* in a state harmless.
var doorbellTypes = map[string]bool{
	"escalation": true,
	"review":     true,
	"done":       true,
}

// doorbell is one child lifecycle transition a coordinator subscribes to,
// identified by (child ticket, seq) and ordered by ts. typ is carried only for the
// wake-reason body.
type doorbell struct {
	child string
	seq   int
	typ   string
	ts    int64 // unix seconds; the event ts, the replay ordering key
}

// doorbellKey is the dedup identity of a doorbell: a `woken-by` activation naming
// the same (child, seq) marks it handled. Seqs are per-attempt and start at 1, and
// the child ticket disambiguates across siblings, so the pair is unique.
func doorbellKey(child string, seq int) string {
	return child + "#" + strconv.Itoa(seq)
}

// activateReverseWants extends the desired set *up* the `wants:` edge (drvctl-041,
// generalised by drvctl-051): it runs the per-coordinator wake machine over every
// `wants:`-carrying ticket. It runs **last in desired(), after the forward and
// coordinator gates**, so a wake overrides a coordinator's own shut
// `after:`/`requires:` gate and re-adds a coordinator the dormancy gate just
// trimmed — a child's lifecycle doorbell is exactly when the coordinator should
// assess.
//
// Withdrawal is not automatic the way the old level-triggered edge's was: a woken
// manager holds an in-flight activation and stays desired until it `park`s (or
// moves itself off Running). That reap-on-park is drvctl-052's completion-gate
// reconciliation; here it is read back as the phase machine below.
func (r *Reconciler) activateReverseWants(all []project.Attempt, out map[worktree.Key]project.Attempt) error {
	edges, err := project.LoadAllEdges(r.opt.Root)
	if err != nil {
		return err
	}

	// Index the latest attempt per ticket (LoadAll is sorted by attempt id, so the
	// last entry wins) — both the coordinator being woken and the children whose logs
	// are scanned for doorbells are read from their latest attempt.
	latestByTicket := make(map[string]project.Attempt, len(all))
	for _, a := range all {
		latestByTicket[a.Ticket] = a
	}

	// The coordinators to consider: every ticket that `wants:` something, in a stable
	// order so any authored event / log line is deterministic across ticks.
	coords := make([]string, 0, len(edges))
	for t, e := range edges {
		if len(e.Wants) > 0 {
			coords = append(coords, t)
		}
	}
	if len(coords) == 0 {
		return nil // no coordinator ⇒ nothing to activate; skip the attempt scan.
	}
	sort.Strings(coords)

	// Liveness is read from the previous tick's run table — exactly the signal the
	// dormant phase needs to tell "just parked, still winding down" (leave for retire)
	// from "already reaped" (admit the next doorbell).
	live := r.liveSet()

	for _, t := range coords {
		w, ok := latestByTicket[t]
		if !ok {
			continue // a coordinator spec with no attempt — nothing to wake.
		}
		if err := r.activateCoordinator(w, edges[t].Wants, latestByTicket, live, out); err != nil {
			return err
		}
	}
	return nil
}

// activateCoordinator runs the serial-replay phase machine for one coordinator W
// (drvctl-051, design decision "activation replay"). It is a no-op unless W is a
// genuine dormant/working coordinator to wake:
//
//   - **Enabled** — a human `disable` is a "leave it alone" signal a child doorbell
//     must not force-override.
//   - **Running** — a non-Running W is either blocked on its own escalation
//     (Needs-me ⇒ suppress while blocked: doorbells queue unhandled until the
//     resolution returns it to Running) or already Review/Done (nothing to wake).
//   - **pre-digest** — the supervision dial (drvctl-042); under passthrough the
//     child's escalation routes straight to the human and W stays a Pending shell.
//
// Then the phase:
//
//   - **active** — W's latest `woken-by` activation has no `park` after it: a
//     doorbell is in flight. Keep W desired (so retire never reaps it mid-handling,
//     whether it is live and working or not-live and resuming after its own
//     escalation resolved) and write nothing — one wake, one doorbell.
//   - **dormant** — W has no activation, or a `park` later than its latest
//     activation. A still-*live* W (just parked, idle session still in the table) is
//     left out of desired so retire reaps it to Pending. Only a *not-live* (reaped)
//     dormant W with a non-empty queue is admitted: author the oldest unhandled
//     doorbell's activation and add W — the one-doorbell-per-wake step.
func (r *Reconciler) activateCoordinator(w project.Attempt, wants []string, latestByTicket map[string]project.Attempt, live map[worktree.Key]bool, out map[worktree.Key]project.Attempt) error {
	if !w.Enabled || w.State != project.Running {
		return nil
	}
	if project.Effective(w.Supervision, r.opt.DefaultSupervision) != project.SupervisionPreDigest {
		return nil
	}

	key := worktree.Key{Ticket: w.Ticket, Attempt: w.ID}

	// Phase from W's own log: the latest activation and the latest park.
	var lastActivationSeq, lastParkSeq int
	handled := map[string]bool{}
	for _, e := range w.Events {
		switch e.Type {
		case wokenByType:
			if e.Seq > lastActivationSeq {
				lastActivationSeq = e.Seq
			}
			if e.WokenChild != "" && e.WokenSeq > 0 {
				handled[doorbellKey(e.WokenChild, e.WokenSeq)] = true
			}
		case "park":
			if e.Seq > lastParkSeq {
				lastParkSeq = e.Seq
			}
		}
	}

	// active: an activation with no park after it ⇒ a doorbell is in flight.
	if lastActivationSeq > 0 && lastParkSeq < lastActivationSeq {
		out[key] = w
		return nil
	}

	// dormant + still live: leave it for retire to reap to Pending; do not write.
	if live[key] {
		return nil
	}

	// dormant + not live: admit the oldest unhandled doorbell, if any.
	queue := pendingDoorbells(wants, latestByTicket, handled)
	if len(queue) == 0 {
		return nil
	}
	d := queue[0]
	if _, err := ticketlog.Append(r.opt.Root, w.Ticket, w.ID, event.Event{
		Type:       wokenByType,
		Actor:      r.opt.Actor,
		WokenChild: d.child,
		WokenSeq:   d.seq,
		Body:       wokenByBody(d),
	}); err != nil {
		return fmt.Errorf("reconcile: author %s on %s/%s: %w", wokenByType, w.Ticket, w.ID, err)
	}
	out[key] = w
	return nil
}

// pendingDoorbells derives the queue: every trigger-type event across the
// coordinator's wanted children (read from each child's latest attempt) with no
// matching activation in `handled`, sorted oldest-first (ties broken by child then
// seq for determinism). The result's head is the next doorbell to replay.
func pendingDoorbells(wants []string, latestByTicket map[string]project.Attempt, handled map[string]bool) []doorbell {
	var q []doorbell
	for _, child := range wants {
		c, ok := latestByTicket[child]
		if !ok {
			continue // a wanted ticket with no attempt yet — no doorbells.
		}
		for _, e := range c.Events {
			if !doorbellTypes[e.Type] {
				continue
			}
			if handled[doorbellKey(child, e.Seq)] {
				continue
			}
			q = append(q, doorbell{child: child, seq: e.Seq, typ: e.Type, ts: e.TS.Unix()})
		}
	}
	sort.Slice(q, func(i, j int) bool {
		if q[i].ts != q[j].ts {
			return q[i].ts < q[j].ts
		}
		if q[i].child != q[j].child {
			return q[i].child < q[j].child
		}
		return q[i].seq < q[j].seq
	})
	return q
}

// wokenByBody renders the human-facing wake reason recorded on the activation
// event — the tail of the manager's cold-start brief (drvweb-023/drvctl-053 render
// it). The board renders event bodies as Markdown, so the child id is backticked.
func wokenByBody(d doorbell) string {
	return fmt.Sprintf("Woken by `%s` #%d — a child `%s` lifecycle doorbell. Authored by the reconciler at wake time: the dedup marker for this doorbell and the wake-reason tail of this brief.", d.child, d.seq, d.typ)
}

// liveSet snapshots the keys currently in the run table (the previous tick's
// actual-state), under the table lock. It is the liveness signal the dormant phase
// reads: present ⇒ a session is (or was just) running for the attempt, so a dormant
// parked coordinator is left for retire; absent ⇒ already reaped, so the next
// doorbell may be admitted.
func (r *Reconciler) liveSet() map[worktree.Key]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := make(map[worktree.Key]bool, len(r.runs))
	for k := range r.runs {
		s[k] = true
	}
	return s
}
