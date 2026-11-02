package systemlog

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/store"
)

// fixedNow returns a deterministic clock for stamping assertions.
func fixedNow(ts string) func() time.Time {
	t, _ := time.Parse(time.RFC3339, ts)
	return func() time.Time { return t }
}

// readEvents reads every system.jsonl record written for an attempt.
func readEvents(t *testing.T, root store.Root, id, att string) []SystemEvent {
	t.Helper()
	f, err := os.Open(root.SessionSystemLogPath(id, att))
	if err != nil {
		t.Fatalf("open system.jsonl: %v", err)
	}
	defer f.Close()
	var evs []SystemEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev SystemEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("unmarshal %q: %v", sc.Text(), err)
		}
		evs = append(evs, ev)
	}
	return evs
}

// TestAppendStampsAndAccumulates: Append stamps the record with the clock's UTC
// RFC3339 time (overwriting any caller value), creates the session dir on demand, and
// accumulates one line per call in order.
func TestAppendStampsAndAccumulates(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id, att := "drv-045", "0001"

	if err := Append(root, id, att, fixedNow("2026-01-02T03:04:05Z"), SystemEvent{
		TS:      "ignored-caller-value",
		Class:   ClassProvenance,
		Message: "provenance updated",
	}); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if err := Append(root, id, att, fixedNow("2026-01-02T03:05:06Z"), SystemEvent{
		Class:   ClassGit,
		Message: "git push: ok",
		Fields:  map[string]string{"verb": "push", "outcome": "ok"},
	}); err != nil {
		t.Fatalf("append 2: %v", err)
	}

	evs := readEvents(t, root, id, att)
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %d", len(evs))
	}
	// The caller-supplied TS is overwritten by the clock, in UTC RFC3339.
	if evs[0].TS != "2026-01-02T03:04:05Z" {
		t.Errorf("event 0 ts = %q, want the clock's stamp, not the caller value", evs[0].TS)
	}
	if evs[0].Class != ClassProvenance || evs[1].Class != ClassGit {
		t.Errorf("classes out of order: %q, %q", evs[0].Class, evs[1].Class)
	}
	if evs[1].Fields["verb"] != "push" {
		t.Errorf("structured fields not round-tripped: %v", evs[1].Fields)
	}
}

// TestAppendRebuildable: deleting system.jsonl does not break a later Append — it
// simply re-creates the file and starts fresh, the drv-045 rebuildable-runtime-state
// contract (safe to delete; refills from subsequent actions).
func TestAppendRebuildable(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id, att := "drv-045", "0001"
	if err := Append(root, id, att, fixedNow("2026-01-02T03:04:05Z"), SystemEvent{Class: ClassGit, Message: "first"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := os.Remove(root.SessionSystemLogPath(id, att)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := Append(root, id, att, fixedNow("2026-01-02T03:05:06Z"), SystemEvent{Class: ClassGit, Message: "second"}); err != nil {
		t.Fatalf("append after delete: %v", err)
	}
	evs := readEvents(t, root, id, att)
	if len(evs) != 1 || evs[0].Message != "second" {
		t.Fatalf("after delete the log should hold only the refilled record, got %+v", evs)
	}
}

// TestAppendNilClock: a nil clock defaults to wallclock rather than panicking, so a
// caller that passes nil still gets a stamped record.
func TestAppendNilClock(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if err := Append(root, "drv-045", "0001", nil, SystemEvent{Class: ClassGit, Message: "x"}); err != nil {
		t.Fatalf("append with nil clock: %v", err)
	}
	evs := readEvents(t, root, "drv-045", "0001")
	if len(evs) != 1 || evs[0].TS == "" {
		t.Fatalf("nil clock should still stamp a ts, got %+v", evs)
	}
}
