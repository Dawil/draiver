package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"draiver/internal/event"
	"draiver/internal/store"
	"draiver/internal/ticketlog"
)

func seedChain(t *testing.T) (store.Root, string) {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}
	id := "PROJ-1"
	if err := root.EnsureTicketDirs(id); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct{ typ, body string }{
		{"created", "start"},
		{"gotcha", "cgo needed"},
		{"escalation", "which base image?"},
		{"resolution", "bookworm"},
	} {
		if _, err := ticketlog.Append(root, id, event.Event{Type: spec.typ, Actor: "a", Body: spec.body}); err != nil {
			t.Fatal(err)
		}
	}
	return root, id
}

func TestVerifyCleanChain(t *testing.T) {
	root, id := seedChain(t)
	res, err := VerifyTicket(root, id)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("clean chain failed: %s", res.Reason)
	}
	if res.Count != 4 {
		t.Errorf("count = %d want 4", res.Count)
	}
}

func TestVerifyDetectsBodyTamper(t *testing.T) {
	root, id := seedChain(t)

	// Rewrite the body of event #2 in place — tamper-evidence must catch it.
	dir := root.LogDir(id)
	entries, _ := os.ReadDir(dir)
	var target string
	for _, e := range entries {
		if strings.Contains(e.Name(), "-0002-") {
			target = filepath.Join(dir, e.Name())
		}
	}
	data, _ := os.ReadFile(target)
	tampered := strings.Replace(string(data), "cgo needed", "cgo not needed", 1)
	if tampered == string(data) {
		t.Fatal("test setup: body substring not found")
	}
	os.WriteFile(target, []byte(tampered), 0o644)

	res, _ := VerifyTicket(root, id)
	if res.OK {
		t.Fatal("tampered chain reported OK")
	}
	if res.BrokenSeq != 2 {
		t.Errorf("broken seq = %d want 2", res.BrokenSeq)
	}
}

func TestVerifyDetectsBrokenLink(t *testing.T) {
	// Two events whose hashes are self-consistent but not linked.
	e1 := event.Event{Seq: 1, Type: "created", Actor: "a", Body: "x", Prev: ""}
	e1.Hash = e1.ComputeHash()
	e2 := event.Event{Seq: 2, Type: "note", Actor: "a", Body: "y", Prev: "not-the-real-prev"}
	e2.Hash = e2.ComputeHash()

	res := Verify([]event.Event{e1, e2})
	if res.OK {
		t.Fatal("broken prev link reported OK")
	}
	if res.BrokenSeq != 2 {
		t.Errorf("broken seq = %d want 2", res.BrokenSeq)
	}
}
