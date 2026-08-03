// Package audit verifies an attempt's hash-chained log: each event must
// recompute to its stored hash and link to its predecessor. Any edit breaks the
// chain.
package audit

import (
	"fmt"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// Result reports whether a chain is intact, and if not, the first broken event.
type Result struct {
	Attempt   string
	OK        bool
	Count     int
	BrokenSeq int    // seq of the first broken event (0 if OK)
	Reason    string // why it broke (empty if OK)
}

// Verify checks events (in seq order) for hash integrity and prev linkage.
func Verify(events []event.Event) Result {
	var prevHash string
	for _, e := range events {
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
		prevHash = e.Hash
	}
	return Result{OK: true, Count: len(events)}
}

// VerifyAttempt reads and verifies one attempt's log.
func VerifyAttempt(root store.Root, ticket, id string) (Result, error) {
	events, err := ticketlog.Read(root, ticket, id)
	if err != nil {
		return Result{}, err
	}
	res := Verify(events)
	res.Attempt = id
	return res, nil
}

// VerifyTicket verifies every attempt's chain on a ticket, returning one result
// per attempt in id order.
func VerifyTicket(root store.Root, ticket string) ([]Result, error) {
	ids, err := root.ListAttempts(ticket)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(ids))
	for _, id := range ids {
		res, err := VerifyAttempt(root, ticket, id)
		if err != nil {
			return nil, err
		}
		results = append(results, res)
	}
	return results, nil
}

func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
