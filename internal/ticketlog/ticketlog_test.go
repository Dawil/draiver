package ticketlog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
)

const att = "0001"

func newTicket(t *testing.T) (store.Root, string) {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}
	id := "PROJ-1"
	if err := root.EnsureAttemptDirs(id, att); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	return root, id
}

func TestAppendAllocatesSeqAndChains(t *testing.T) {
	root, id := newTicket(t)

	e1, err := Append(root, id, att, event.Event{Type: "created", Actor: "human:dave", Body: "start"})
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if e1.Seq != 1 || e1.Prev != "" {
		t.Fatalf("genesis: seq=%d prev=%q", e1.Seq, e1.Prev)
	}
	if e1.Attempt != att {
		t.Errorf("attempt not stamped: %q", e1.Attempt)
	}

	e2, err := Append(root, id, att, event.Event{Type: "gotcha", Actor: "agent:x", Body: "bit me"})
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if e2.Seq != 2 {
		t.Errorf("seq = %d want 2", e2.Seq)
	}
	if e2.Prev != e1.Hash {
		t.Errorf("prev = %q want %q", e2.Prev, e1.Hash)
	}
}

func TestChainsAreIndependentPerAttempt(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	id := "PROJ-1"
	for _, a := range []string{"0001", "0002"} {
		if err := root.EnsureAttemptDirs(id, a); err != nil {
			t.Fatal(err)
		}
	}
	a1, _ := Append(root, id, "0001", event.Event{Type: "created", Actor: "a", Body: "x"})
	b1, _ := Append(root, id, "0002", event.Event{Type: "created", Actor: "a", Body: "x"})
	// Each attempt's chain starts fresh at seq 1 with an empty prev.
	if a1.Seq != 1 || b1.Seq != 1 {
		t.Fatalf("seqs = %d,%d want 1,1", a1.Seq, b1.Seq)
	}
	if a1.Prev != "" || b1.Prev != "" {
		t.Errorf("genesis prev must be empty in both attempts")
	}
	// Same body but different attempt id => different hash (attempt is in the chain).
	if a1.Hash == b1.Hash {
		t.Errorf("attempts share a hash despite different attempt ids")
	}
}

func TestReadReturnsCausalOrder(t *testing.T) {
	root, id := newTicket(t)
	for i := 0; i < 5; i++ {
		if _, err := Append(root, id, att, event.Event{Type: "note", Actor: "a", Body: "n"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	events, err := Read(root, id, att)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("got %d events want 5", len(events))
	}
	for i, e := range events {
		if e.Seq != i+1 {
			t.Errorf("events[%d].Seq = %d want %d", i, e.Seq, i+1)
		}
	}
}

func TestSameSecondEventsOrderBySeq(t *testing.T) {
	root, id := newTicket(t)
	ts := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if _, err := Append(root, id, att, event.Event{Type: "note", Actor: "a", TS: ts, Body: "same second"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	events, err := Read(root, id, att)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d want 3 (same-second events must not collide)", len(events))
	}
	for i, e := range events {
		if e.Seq != i+1 {
			t.Errorf("order broken: events[%d].Seq=%d", i, e.Seq)
		}
	}
}

func TestWriteOnceNeverClobbers(t *testing.T) {
	root, id := newTicket(t)
	e, err := Append(root, id, att, event.Event{Type: "note", Actor: "a", Body: "original"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	path := filepath.Join(root.LogDir(id, att), e.Filename())
	before, _ := os.ReadFile(path)

	if _, err := Append(root, id, att, event.Event{Type: "note", Actor: "a", Body: "second"}); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("existing event file was modified by a later append")
	}
}

func TestAppendRejectsUnknownAttempt(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if _, err := Append(root, "NOPE-1", att, event.Event{Type: "note", Actor: "a"}); err == nil {
		t.Error("expected error appending to nonexistent attempt")
	}
}
