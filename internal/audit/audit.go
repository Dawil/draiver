// Package audit verifies a ticket's hash-chained log: each event must recompute
// to its stored hash and link to its predecessor. Any edit breaks the chain.
package audit

import (
	"fmt"

	"draiver/internal/event"
	"draiver/internal/store"
	"draiver/internal/ticketlog"
)

// Result reports whether a chain is intact, and if not, the first broken event.
type Result struct {
	OK        bool
	Count     int
	BrokenSeq int    // seq of the first broken event (0 if OK)
	Reason    string // why it broke (empty if OK)
}

// Verify checks events (in seq order) for hash integrity and prev linkage.
func Verify(events []event.Event) Result {
	var prevHash string
	for i, e := range events {
		if e.Prev != prevHash {
			return Result{
				OK:        false,
				Count:     len(events),
				BrokenSeq: e.Seq,
				Reason: fmt.Sprintf("prev mismatch: event #%d records prev %q but predecessor hash is %q",
					e.Seq, short(e.Prev), short(prevHash)),
			}
		}
		if got := e.ComputeHash(); got != e.Hash {
			return Result{
				OK:        false,
				Count:     len(events),
				BrokenSeq: e.Seq,
				Reason: fmt.Sprintf("hash mismatch at event #%d: stored %q, recomputed %q (content edited)",
					e.Seq, short(e.Hash), short(got)),
			}
		}
		_ = i
		prevHash = e.Hash
	}
	return Result{OK: true, Count: len(events)}
}

// VerifyTicket reads and verifies a ticket's log.
func VerifyTicket(root store.Root, id string) (Result, error) {
	events, err := ticketlog.Read(root, id)
	if err != nil {
		return Result{}, err
	}
	return Verify(events), nil
}

func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
