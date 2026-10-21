// Package recallmetric is the read-side instrumentation behind `draiver recall`
// (drv-036). The codebase-ledger epic (drv-035) rests on one unproven claim — that
// accumulated belief actually gets read by later agents — so before building the
// feature we measure today's baseline: every recall appends one line to a root-level
// sink naming the ticket/attempt read, the read mode, and the seqs surfaced. Folding
// those lines answers "how often is ticket X recalled" and "how often is fact X#N
// pulled" — consumption, and the ranking of which facts carry weight.
//
// It is deliberately offline and dependency-free (stdlib only, no service, no daemon),
// consistent with `recall` being a pure read over the on-disk log. The sink lives at
// the data root, out of any attempt's hash chain: it is observability, not truth —
// rebuildable and safe to delete.
package recallmetric

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/Dawil/draiver/internal/store"
)

// Mode names what a recall invocation surfaced: the live index, specific seq bodies,
// or a fact's supersession chain. The `--tags` summary surfaces tags rather than
// individual facts, so it is intentionally not recorded (nothing to rank per-fact).
const (
	ModeIndex = "index"
	ModeSeq   = "seq"
	ModeChain = "chain"
)

// Record is one recall invocation, serialised as a single JSON line in the sink.
// Seqs are the seqs actually surfaced (shown in the index, pulled as bodies, or walked
// in the chain) — the per-fact signal. TS is stamped at append time when zero.
type Record struct {
	TS      time.Time `json:"ts"`
	Actor   string    `json:"actor"`
	Ticket  string    `json:"ticket"`
	Attempt string    `json:"attempt"`
	Mode    string    `json:"mode"`
	Seqs    []int     `json:"seqs"`
}

// Append writes one record to the root's recall-metrics sink as a JSON line. It is
// best-effort by contract: callers on `recall`'s hot path ignore the error so a
// read-only or full data root can never change recall's output or exit (drv-036 AC4).
// A zero TS is stamped with the current UTC time so callers need not pass a clock.
func Append(root store.Root, rec Record) error {
	if rec.TS.IsZero() {
		rec.TS = time.Now().UTC()
	}
	if rec.Seqs == nil {
		rec.Seqs = []int{}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal recall metric: %w", err)
	}
	f, err := os.OpenFile(root.RecallMetricsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open recall metrics sink: %w", err)
	}
	defer f.Close()
	// One write syscall of a small line under O_APPEND is atomic on POSIX, so
	// concurrent recalls interleave whole lines rather than corrupting each other.
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append recall metric: %w", err)
	}
	return nil
}

// Read returns every record in the root's sink, oldest-first (file order). A missing
// sink is an empty history, not an error — no recall has been recorded yet. Malformed
// lines are skipped rather than failing the whole read, so a partially-written tail
// (a crash mid-append) never blinds the stats surface to the records before it.
func Read(root store.Root) ([]Record, error) {
	f, err := os.Open(root.RecallMetricsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open recall metrics sink: %w", err)
	}
	defer f.Close()
	var recs []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // tolerate a torn line rather than losing the whole history
		}
		recs = append(recs, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan recall metrics sink: %w", err)
	}
	return recs, nil
}

// FactKey identifies a fact across the sink: ticket, attempt, and seq.
type FactKey struct {
	Ticket  string
	Attempt string
	Seq     int
}

// TicketCount is a ticket's recall frequency, for sorted reporting.
type TicketCount struct {
	Ticket string
	Count  int
}

// FactCount is a single fact's pull frequency, for sorted reporting.
type FactCount struct {
	Key   FactKey
	Count int
}

// Stats is the fold over the sink: the total number of recall invocations, each
// ticket's recall count, and each fact's pull count — the two questions the
// instrumentation exists to answer. Both slices are sorted most-recalled-first (ties
// broken by name/seq for determinism).
type Stats struct {
	Total   int
	Tickets []TicketCount
	Facts   []FactCount
}

// Aggregate folds records into per-ticket and per-fact counts. A ticket is counted
// once per invocation that read it; a fact is counted once per invocation that
// surfaced its seq (an index listing it, a body pull of it, or a chain walking it).
// Optionally scoped to one ticket (pass "" for all).
func Aggregate(recs []Record, ticketFilter string) Stats {
	ticketCounts := map[string]int{}
	factCounts := map[FactKey]int{}
	var total int
	for _, r := range recs {
		if ticketFilter != "" && r.Ticket != ticketFilter {
			continue
		}
		total++
		ticketCounts[r.Ticket]++
		for _, s := range r.Seqs {
			factCounts[FactKey{Ticket: r.Ticket, Attempt: r.Attempt, Seq: s}]++
		}
	}

	tickets := make([]TicketCount, 0, len(ticketCounts))
	for t, c := range ticketCounts {
		tickets = append(tickets, TicketCount{Ticket: t, Count: c})
	}
	sort.Slice(tickets, func(i, j int) bool {
		if tickets[i].Count != tickets[j].Count {
			return tickets[i].Count > tickets[j].Count
		}
		return tickets[i].Ticket < tickets[j].Ticket
	})

	facts := make([]FactCount, 0, len(factCounts))
	for k, c := range factCounts {
		facts = append(facts, FactCount{Key: k, Count: c})
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].Count != facts[j].Count {
			return facts[i].Count > facts[j].Count
		}
		if facts[i].Key.Ticket != facts[j].Key.Ticket {
			return facts[i].Key.Ticket < facts[j].Key.Ticket
		}
		if facts[i].Key.Attempt != facts[j].Key.Attempt {
			return facts[i].Key.Attempt < facts[j].Key.Attempt
		}
		return facts[i].Key.Seq < facts[j].Key.Seq
	})

	return Stats{Total: total, Tickets: tickets, Facts: facts}
}
