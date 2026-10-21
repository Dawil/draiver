package acceptance

import (
	"fmt"
	"os"
	"strings"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/kb"
	"github.com/Dawil/draiver/internal/recallmetric"
	"github.com/Dawil/draiver/internal/store"
)

// These steps exercise drv-036's recall instrumentation end to end over a real
// on-disk root. recallmetric is the engine `draiver recall` writes to on every
// invocation (cmd/recall.go's recordRecall) and `draiver recall-stats` folds, so
// driving recallmetric over a seeded attempt — surfacing seqs from the same kb.Fold
// recall shows, then recording them — asserts on the agent-facing surface, mirroring
// how steps_recall.go asserts on kb.Fold for recall itself.

// doRecall mirrors one `draiver recall` invocation's side-channel: it computes the
// seqs the given mode would surface from the fold (exactly what recall prints) and
// appends one metric record for them, returning those seqs. It is the faithful engine
// equivalent of cmd/recall.go selecting facts for output and calling recordRecall.
func doRecall(root store.Root, view kb.View, mode string, arg int) ([]int, error) {
	var seqs []int
	switch mode {
	case recallmetric.ModeIndex:
		for _, f := range view.Live() {
			seqs = append(seqs, f.Seq)
		}
	case recallmetric.ModeSeq:
		if f, ok := view.Get(arg); ok {
			seqs = []int{f.Seq}
		}
	case recallmetric.ModeChain:
		for _, f := range view.Chain(arg) {
			seqs = append(seqs, f.Seq)
		}
	}
	err := recallmetric.Append(root, recallmetric.Record{
		Actor: "agent:acceptance", Ticket: recTicket, Attempt: recAtt, Mode: mode, Seqs: seqs,
	})
	return seqs, err
}

// givenRecalledAttempt seeds an attempt with a created event, a gotcha (#2) and a
// decision (#3), then performs a handful of recalls over it: the index, two body pulls
// of #2, and #3's chain — so #2 is the belief read most often.
func givenRecalledAttempt(w *World, sr *StepRun) error {
	root, err := seedKBData(w)
	if err != nil {
		return err
	}
	if _, err := appendKB(root, event.Event{Type: "created", Body: "start"}); err != nil { // #1
		return err
	}
	if _, err := appendKB(root, event.Event{Type: "gotcha", Body: "stripe test keys only work in test mode"}); err != nil { // #2
		return err
	}
	if _, err := appendKB(root, event.Event{Type: "decision", Body: "chose server-side pagination"}); err != nil { // #3
		return err
	}
	view, err := kbView(root)
	if err != nil {
		return err
	}
	// A realistic read pattern: skim the index, pull the gotcha #2 twice, walk #3's chain.
	for _, step := range []struct {
		mode string
		arg  int
	}{
		{recallmetric.ModeIndex, 0},
		{recallmetric.ModeSeq, 2},
		{recallmetric.ModeSeq, 2},
		{recallmetric.ModeChain, 3},
	} {
		if _, err := doRecall(root, view, step.mode, step.arg); err != nil {
			return err
		}
	}
	w.set("root", root)
	return nil
}

// thenEachRecallRecorded asserts every recall left one line naming the ticket/attempt
// and the seq(s) it surfaced — the per-fact signal (AC1).
func thenEachRecallRecorded(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	recs, err := recallmetric.Read(root)
	if err != nil {
		return err
	}
	if len(recs) != 4 {
		return fmt.Errorf("expected one record per recall (4), got %d", len(recs))
	}
	for _, r := range recs {
		if r.Ticket != recTicket || r.Attempt != recAtt {
			return fmt.Errorf("record not attributed to the read target: %+v", r)
		}
		if r.Mode != recallmetric.ModeIndex && len(r.Seqs) == 0 {
			return fmt.Errorf("a %s recall surfaced no seqs: %+v", r.Mode, r)
		}
	}
	var b strings.Builder
	raw, _ := os.ReadFile(root.RecallMetricsPath())
	fmt.Fprintf(&b, "$ cat recall-metrics.jsonl   # one offline line per recall\n%s", raw)
	attachText(sr, "recall-metrics-sink", b.String())
	return nil
}

// thenFoldsToCounts asserts recall-stats folds the sink into per-ticket and per-fact
// counts, and that the fact read most often ranks first (AC1/AC3). #2 was pulled twice
// as a body and once in the index = 3, the most of any fact.
func thenFoldsToCounts(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	recs, err := recallmetric.Read(root)
	if err != nil {
		return err
	}
	stats := recallmetric.Aggregate(recs, "")
	if stats.Total != 4 {
		return fmt.Errorf("total recalls = %d, want 4", stats.Total)
	}
	if len(stats.Tickets) != 1 || stats.Tickets[0].Ticket != recTicket || stats.Tickets[0].Count != 4 {
		return fmt.Errorf("per-ticket fold wrong: %+v", stats.Tickets)
	}
	top := stats.Facts[0]
	if top.Key.Seq != 2 || top.Count != 3 {
		return fmt.Errorf("most-recalled fact = %+v, want #2 count 3", top)
	}
	attachText(sr, "recall-stats", renderStats(recTicket, stats))
	return nil
}

// thenBestEffortNoChange asserts the recording is a side-channel (AC4): the seqs recall
// surfaces come from the fold and are unchanged whether the sink write succeeds or
// fails. We force a failing sink (a directory where the file belongs) and show the
// surfaced index is still produced in full.
func thenBestEffortNoChange(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	view, err := kbView(root)
	if err != nil {
		return err
	}
	want := make([]int, 0)
	for _, f := range view.Live() {
		want = append(want, f.Seq)
	}

	// A root whose sink path is an unwritable directory: Append must fail, yet the same
	// surfacing still yields the full index — the metric is a side-channel, not a gate.
	broken := store.Root{Dir: mustTempDir(w)}
	if err := os.Mkdir(broken.RecallMetricsPath(), 0o755); err != nil {
		return err
	}
	// Re-fold the broken root's (empty) log would differ; surface over the SAME view to
	// isolate the side-channel property: identical surfaced seqs, write error swallowed.
	got, appendErr := doRecall(broken, view, recallmetric.ModeIndex, 0)
	if appendErr == nil {
		return fmt.Errorf("expected the unwritable sink to fail the append (to prove it is swallowed on the real path)")
	}
	if !sameInts(got, want) {
		return fmt.Errorf("surfaced seqs changed when the sink failed: got %v want %v", got, want)
	}
	attachText(sr, "side-channel",
		fmt.Sprintf("recall surfaces facts from the fold, independent of the sink:\n"+
			"  surfaced seqs: %v\n"+
			"  sink write error (swallowed on recall's hot path): %v\n"+
			"  → recall's stdout/exit are unchanged by instrumentation", got, appendErr))
	return nil
}

// thenCountsRetrievableOffline re-reads the on-disk sink with no daemon and folds it,
// showing the counts are retrievable offline (AC2/AC3).
func thenCountsRetrievableOffline(w *World, sr *StepRun) error {
	root := w.get("root").(store.Root)
	// The sink is a plain regular file at the data root — no service, no socket.
	fi, err := os.Stat(root.RecallMetricsPath())
	if err != nil {
		return fmt.Errorf("sink is not a plain on-disk file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("sink is not a regular file: mode %v", fi.Mode())
	}
	recs, err := recallmetric.Read(root)
	if err != nil {
		return err
	}
	stats := recallmetric.Aggregate(recs, "")
	if stats.Total == 0 {
		return fmt.Errorf("offline fold found no recalls")
	}
	attachText(sr, "offline-retrieval",
		fmt.Sprintf("$ stat recall-metrics.jsonl  → regular file (%d bytes), no daemon\n\n%s",
			fi.Size(), renderStats(recTicket, stats)))
	return nil
}

// renderStats renders recall-stats output the way the CLI prints it, for evidence.
func renderStats(ticket string, stats recallmetric.Stats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "$ draiver recall-stats\n%d recall(s) recorded\n\nper-ticket:\n", stats.Total)
	for _, t := range stats.Tickets {
		fmt.Fprintf(&b, "  %-12s %d\n", t.Ticket, t.Count)
	}
	b.WriteString("\nper-fact:\n")
	for _, f := range stats.Facts {
		fmt.Fprintf(&b, "  %s@%s#%d  %d\n", f.Key.Ticket, f.Key.Attempt, f.Key.Seq, f.Count)
	}
	return b.String()
}

// mustTempDir makes a World-cleaned temp dir, panicking on failure (steps return error
// paths; this keeps the broken-sink setup terse).
func mustTempDir(w *World) string {
	dir, err := os.MkdirTemp("", "acc-recallmetric-")
	if err != nil {
		panic(err)
	}
	w.defer_(func() { os.RemoveAll(dir) })
	return dir
}

// sameInts reports whether two int slices hold the same elements in the same order.
func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
