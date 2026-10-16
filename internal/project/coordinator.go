package project

import "strconv"

// WakeEventType is the event type the deterministic spine appends to a
// coordinator's own log at wake time (drvctl-051): a `woken by C #N` activation.
// It is the control plane's recorded wake reason — one source, three consumers
// (dedup, brief, board). drvweb-023 is the board consumer: it reads the latest
// activation for the "why" and derives the queue depth for the count. Kept in sync
// with internal/reconcile's writer-side constant of the same value.
const WakeEventType = "woken-by"

// coordinatorDoorbellTypes is the trigger set: the child-log lifecycle transitions
// a coordinator subscribes to (drvctl-051). It mirrors the reconciler's own
// `doorbellTypes`; the board derives its queue depth independently from the same
// child events rather than importing the daemon, which is the design's "one
// activation event, three consumers" — so the set is duplicated here deliberately,
// not shared.
var coordinatorDoorbellTypes = map[string]bool{
	"escalation": true,
	"review":     true,
	"done":       true,
}

// IsCoordinator reports whether a ticket is a coordinator — a ticket that `wants:`
// something. It is the same gate the capability panel and reverse-`wants:`
// activation render behind (len(edges.Wants) > 0), named once so the board, the
// panel, and the daemon agree on what a manager is.
func IsCoordinator(ticket string, edges map[string]Edges) bool {
	return len(edges[ticket].Wants) > 0
}

// CoordinatorSignal is the board's read of a coordinator's control-plane wake
// state (drvweb-023): the child that most recently woke it (the "why") and the
// number of child doorbells still queued behind the current activation (the
// derived depth). It is a pure projection over the already-loaded logs — the same
// derivation the reconciler's reverse_wants.go performs, read independently.
type CoordinatorSignal struct {
	// Woken is true when the coordinator has a `woken-by` activation that no later
	// `park` has retired — an in-flight wake. WakeChild is only meaningful then.
	Woken bool
	// WakeChild is the child named by the latest `woken-by` activation — the "why"
	// the board renders ("manager — on it, re: <WakeChild>"). Empty when the
	// coordinator has never been woken (or has parked since its last wake).
	WakeChild string
	// QueueDepth is the count of child doorbells (escalation/review/done on the
	// wanted children's latest attempts) with no matching activation on the
	// coordinator's own log — the "· N queued" count. It is independent of the
	// active/parked phase: the current activation's own doorbell is already handled,
	// so the depth is exactly what waits behind the watermark.
	QueueDepth int
}

// DeriveCoordinatorSignal projects a coordinator's wake state from the already
// loaded fleet. coord is the coordinator attempt (its own log carries the
// `woken-by` activations and any `park`); all is the fleet (children are read from
// their latest attempt); edges supplies coord's `wants:` set. It replicates the
// reconciler's activation scan (reverse_wants.go) as a read-only board derivation.
func DeriveCoordinatorSignal(coord Attempt, all []Attempt, edges map[string]Edges) CoordinatorSignal {
	wants := edges[coord.Ticket].Wants

	// Phase from the coordinator's own log: the latest activation (its child is the
	// "why"), the set of handled (child, seq) doorbells, and the latest park.
	var lastActSeq, lastParkSeq int
	var wakeChild string
	handled := map[string]bool{}
	for _, e := range coord.Events {
		switch e.Type {
		case WakeEventType:
			if e.Seq > lastActSeq {
				lastActSeq = e.Seq
				wakeChild = e.WokenChild
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

	// Queue depth: trigger-type events across the wanted children's latest attempts
	// with no matching activation. Latest attempt per ticket — LoadAll is sorted by
	// ticket then attempt id, so the last entry seen wins, exactly as board() folds.
	latest := make(map[string]Attempt, len(all))
	for _, a := range all {
		latest[a.Ticket] = a
	}
	depth := 0
	for _, child := range wants {
		c, ok := latest[child]
		if !ok {
			continue // a wanted ticket with no attempt yet — no doorbells.
		}
		for _, e := range c.Events {
			if !coordinatorDoorbellTypes[e.Type] {
				continue
			}
			if handled[doorbellKey(child, e.Seq)] {
				continue
			}
			depth++
		}
	}

	sig := CoordinatorSignal{QueueDepth: depth}
	// Active ⇒ a `woken-by` with no later `park`. A still-live coordinator that has
	// parked is winding down, not "on it", so its why is not surfaced as in-flight.
	if lastActSeq > 0 && lastParkSeq < lastActSeq {
		sig.Woken = true
		sig.WakeChild = wakeChild
	}
	return sig
}

// doorbellKey is the dedup identity of a doorbell: the (child, seq) pair. It
// matches reverse_wants.go's identity so the board's handled-set check agrees with
// the spine's. Seqs are per-attempt from 1 and the child disambiguates siblings.
func doorbellKey(child string, seq int) string {
	return child + "#" + strconv.Itoa(seq)
}
