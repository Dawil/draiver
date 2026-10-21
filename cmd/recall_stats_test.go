package cmd

import (
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/recallmetric"
	"github.com/Dawil/draiver/internal/store"
)

// drv-036 AC1/AC2: every recall invocation records which ticket/attempt and which
// seq(s) it surfaced, to a root-level offline sink — no runtime service, recall stays
// a pure read. The index, body and chain modes each record the seqs they showed.
func TestRecallRecordsEveryInvocation(t *testing.T) {
	dir := newTicket(t)
	actor := []string{"--data", dir, "--actor", "agent:x"}
	run(t, append(actor, "log", "PROJ-1", "first gotcha", "--type", "gotcha")...)   // #2
	run(t, append(actor, "log", "PROJ-1", "a decision", "--type", "decision")...)   // #3

	// Three distinct reads: the index, a body pull of #3, and #3's chain.
	run(t, "--data", dir, "recall", "PROJ-1")
	run(t, "--data", dir, "recall", "PROJ-1", "3")
	run(t, "--data", dir, "recall", "PROJ-1", "--chain", "3")

	recs, err := recallmetric.Read(store.Root{Dir: dir})
	if err != nil {
		t.Fatalf("read sink: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("want one record per recall (3), got %d: %+v", len(recs), recs)
	}
	modes := map[string]recallmetric.Record{}
	for _, r := range recs {
		modes[r.Mode] = r
		if r.Ticket != "PROJ-1" || r.Attempt != "0001" {
			t.Errorf("record not attributed to the read target: %+v", r)
		}
	}
	// The index listed the live facts #1..#3.
	if idx, ok := modes[recallmetric.ModeIndex]; !ok || len(idx.Seqs) != 3 {
		t.Errorf("index record should surface 3 live seqs: %+v", idx)
	}
	// The body pull and chain each named #3.
	if seq, ok := modes[recallmetric.ModeSeq]; !ok || len(seq.Seqs) != 1 || seq.Seqs[0] != 3 {
		t.Errorf("seq record should surface #3: %+v", seq)
	}
	if ch, ok := modes[recallmetric.ModeChain]; !ok || len(ch.Seqs) == 0 || ch.Seqs[len(ch.Seqs)-1] != 3 {
		t.Errorf("chain record should surface the chain ending at #3: %+v", ch)
	}
}

// drv-036 AC4: instrumentation is a side-channel — it must not change recall's output
// or its exit. The index output is byte-identical whether or not the sink exists, and
// the `--tags` summary (which surfaces tags, not facts) records nothing.
func TestRecallInstrumentationIsSideChannel(t *testing.T) {
	dir := newTicket(t)
	run(t, "--data", dir, "--actor", "agent:x", "log", "PROJ-1", "tagged", "--type", "gotcha", "--tag", "auth")

	out1, code1 := run(t, "--data", dir, "recall", "PROJ-1")
	out2, code2 := run(t, "--data", dir, "recall", "PROJ-1")
	if code1 != 0 || code2 != 0 {
		t.Fatalf("recall exit changed: %d then %d", code1, code2)
	}
	if out1 != out2 {
		t.Errorf("recall output changed once the sink existed:\n%q\nvs\n%q", out1, out2)
	}

	// --tags surfaces tags, not individual facts: deliberately not recorded.
	before, _ := recallmetric.Read(store.Root{Dir: dir})
	run(t, "--data", dir, "recall", "PROJ-1", "--tags")
	after, _ := recallmetric.Read(store.Root{Dir: dir})
	if len(after) != len(before) {
		t.Errorf("--tags should record nothing: %d -> %d", len(before), len(after))
	}
}

// A usage/validation error is not a recall: a missing seq errors as before and writes
// no metric line.
func TestRecallErrorRecordsNothing(t *testing.T) {
	dir := newTicket(t)
	out, code, _ := runE(t, "--data", dir, "recall", "PROJ-1", "999")
	if code == 0 {
		t.Fatalf("recall of a missing seq should error:\n%s", out)
	}
	recs, _ := recallmetric.Read(store.Root{Dir: dir})
	if len(recs) != 0 {
		t.Errorf("a failed recall should write no metric: %+v", recs)
	}
}

// drv-036 AC3: the counts are retrievable well enough to answer "which facts on which
// tickets get recalled, and how often" — recall-stats ranks per-ticket and per-fact.
func TestRecallStatsAggregates(t *testing.T) {
	dir := newTicket(t)
	run(t, "--data", dir, "--actor", "agent:x", "log", "PROJ-1", "a gotcha", "--type", "gotcha") // #2

	// Pull #2 twice and the index once.
	run(t, "--data", dir, "recall", "PROJ-1", "2")
	run(t, "--data", dir, "recall", "PROJ-1", "2")
	run(t, "--data", dir, "recall", "PROJ-1")

	out, code := run(t, "--data", dir, "recall-stats")
	if code != 0 {
		t.Fatalf("recall-stats exited %d: %s", code, out)
	}
	if !strings.Contains(out, "3 recall(s) recorded") {
		t.Errorf("stats total wrong:\n%s", out)
	}
	if !strings.Contains(out, "per-ticket:") || !strings.Contains(out, "PROJ-1") {
		t.Errorf("stats missing per-ticket fold:\n%s", out)
	}
	// #2 pulled twice as a body plus once in the index = 3; it is the top-ranked fact.
	if !strings.Contains(out, "PROJ-1@0001#2") {
		t.Errorf("stats missing the most-pulled fact PROJ-1@0001#2:\n%s", out)
	}
	perFact := out[strings.Index(out, "per-fact:"):]
	if i2 := strings.Index(perFact, "#2"); i2 < 0 {
		t.Errorf("per-fact list missing #2:\n%s", out)
	}

	// Empty root reports cleanly, not an error.
	empty, code := run(t, "--data", t.TempDir(), "recall-stats")
	if code != 0 {
		t.Fatalf("recall-stats on an empty root exited %d", code)
	}
	if !strings.Contains(empty, "no recalls recorded") {
		t.Errorf("empty stats message wrong:\n%s", empty)
	}
}
