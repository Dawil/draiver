package ticketlog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"draiver/internal/event"
	"draiver/internal/store"
)

func newTicket(t *testing.T) (store.Root, string) {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}
	id := "PROJ-1"
	if err := root.EnsureTicketDirs(id); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	return root, id
}

func TestAppendAllocatesSeqAndChains(t *testing.T) {
	root, id := newTicket(t)

	e1, err := Append(root, id, event.Event{Type: "created", Actor: "human:dave", Body: "start"})
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if e1.Seq != 1 || e1.Prev != "" {
		t.Fatalf("genesis: seq=%d prev=%q", e1.Seq, e1.Prev)
	}

	e2, err := Append(root, id, event.Event{Type: "gotcha", Actor: "agent:x", Body: "bit me"})
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

func TestReadReturnsCausalOrder(t *testing.T) {
	root, id := newTicket(t)
	for i := 0; i < 5; i++ {
		if _, err := Append(root, id, event.Event{Type: "note", Actor: "a", Body: "n"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	events, err := Read(root, id)
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
		if _, err := Append(root, id, event.Event{Type: "note", Actor: "a", TS: ts, Body: "same second"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	events, err := Read(root, id)
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
	e, err := Append(root, id, event.Event{Type: "note", Actor: "a", Body: "original"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	path := filepath.Join(root.LogDir(id), e.Filename())
	before, _ := os.ReadFile(path)

	// A second append must land on a new seq/file, leaving the first byte-identical.
	if _, err := Append(root, id, event.Event{Type: "note", Actor: "a", Body: "second"}); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("existing event file was modified by a later append")
	}
}

func TestAppendRejectsUnknownTicket(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	if _, err := Append(root, "NOPE-1", event.Event{Type: "note", Actor: "a"}); err == nil {
		t.Error("expected error appending to nonexistent ticket")
	}
}
