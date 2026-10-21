package recallmetric

import (
	"os"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/store"
)

func tmpRoot(t *testing.T) store.Root {
	t.Helper()
	return store.Root{Dir: t.TempDir()}
}

// Append then Read round-trips records in file (oldest-first) order, stamps a TS when
// the caller omits it, and normalises a nil Seqs to an empty slice.
func TestAppendReadRoundTrip(t *testing.T) {
	root := tmpRoot(t)
	if err := Append(root, Record{Ticket: "A", Attempt: "0001", Mode: ModeIndex, Seqs: []int{1, 3}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := Append(root, Record{Ticket: "A", Attempt: "0001", Mode: ModeSeq, Seqs: nil}); err != nil {
		t.Fatalf("append: %v", err)
	}
	recs, err := Read(root)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	if recs[0].Mode != ModeIndex || len(recs[0].Seqs) != 2 {
		t.Errorf("first record wrong: %+v", recs[0])
	}
	if recs[1].Seqs == nil || len(recs[1].Seqs) != 0 {
		t.Errorf("nil seqs should normalise to empty, got %+v", recs[1].Seqs)
	}
	if recs[0].TS.IsZero() {
		t.Error("TS should be stamped at append time")
	}
}

// A missing sink is an empty history, not an error — nothing has been recalled yet.
func TestReadMissingSinkIsEmpty(t *testing.T) {
	recs, err := Read(tmpRoot(t))
	if err != nil {
		t.Fatalf("missing sink should not error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("want empty history, got %d", len(recs))
	}
}

// A torn/garbage line is skipped so the records around it still read — a crash
// mid-append must not blind the stats surface.
func TestReadToleratesTornLine(t *testing.T) {
	root := tmpRoot(t)
	Append(root, Record{Ticket: "A", Mode: ModeIndex, Seqs: []int{1}})
	f, _ := os.OpenFile(root.RecallMetricsPath(), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("{not valid json\n")
	f.Close()
	Append(root, Record{Ticket: "B", Mode: ModeIndex, Seqs: []int{2}})

	recs, err := Read(root)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("torn line should be skipped, got %d records", len(recs))
	}
	if recs[0].Ticket != "A" || recs[1].Ticket != "B" {
		t.Errorf("surviving records out of order: %+v", recs)
	}
}

// Aggregate folds to per-ticket and per-fact counts, sorted most-recalled-first, and
// an invocation counts a fact once per seq it surfaced.
func TestAggregateCountsAndRanks(t *testing.T) {
	recs := []Record{
		{Ticket: "A", Attempt: "0001", Mode: ModeIndex, Seqs: []int{1, 2}},
		{Ticket: "A", Attempt: "0001", Mode: ModeSeq, Seqs: []int{2}},
		{Ticket: "A", Attempt: "0001", Mode: ModeChain, Seqs: []int{2, 5}},
		{Ticket: "B", Attempt: "0001", Mode: ModeIndex, Seqs: []int{9}},
	}
	st := Aggregate(recs, "")
	if st.Total != 4 {
		t.Errorf("total = %d, want 4", st.Total)
	}
	// Per-ticket: A recalled 3×, B 1× — A ranks first.
	if len(st.Tickets) != 2 || st.Tickets[0].Ticket != "A" || st.Tickets[0].Count != 3 {
		t.Errorf("per-ticket wrong: %+v", st.Tickets)
	}
	if st.Tickets[1].Ticket != "B" || st.Tickets[1].Count != 1 {
		t.Errorf("per-ticket B wrong: %+v", st.Tickets)
	}
	// Per-fact: A#2 surfaced in all three A invocations → the top-ranked belief.
	top := st.Facts[0]
	if top.Key.Ticket != "A" || top.Key.Seq != 2 || top.Count != 3 {
		t.Errorf("top fact = %+v, want A@0001#2 count 3", top)
	}
}

// A ticket filter scopes both folds to that ticket.
func TestAggregateTicketFilter(t *testing.T) {
	recs := []Record{
		{Ticket: "A", Attempt: "0001", Mode: ModeIndex, Seqs: []int{1}},
		{Ticket: "B", Attempt: "0001", Mode: ModeIndex, Seqs: []int{1}},
	}
	st := Aggregate(recs, "A")
	if st.Total != 1 || len(st.Tickets) != 1 || st.Tickets[0].Ticket != "A" {
		t.Errorf("filter to A failed: %+v", st)
	}
	for _, f := range st.Facts {
		if f.Key.Ticket != "A" {
			t.Errorf("filtered fold leaked ticket %q", f.Key.Ticket)
		}
	}
}

// The sink is a JSON-lines file: one object per line, greppable by ticket (AC3's
// lower-tech path).
func TestSinkIsGreppableJSONLines(t *testing.T) {
	root := tmpRoot(t)
	Append(root, Record{Ticket: "drv-036", Attempt: "0001", Mode: ModeIndex, Seqs: []int{1}})
	b, err := os.ReadFile(root.RecallMetricsPath())
	if err != nil {
		t.Fatalf("read sink: %v", err)
	}
	line := strings.TrimSpace(string(b))
	if strings.Count(line, "\n") != 0 {
		t.Errorf("one append should write one line, got %q", line)
	}
	if !strings.Contains(line, `"ticket":"drv-036"`) || !strings.Contains(line, `"mode":"index"`) {
		t.Errorf("line not greppable by ticket/mode: %q", line)
	}
}
