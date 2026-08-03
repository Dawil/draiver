package cmd

import (
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func TestAttemptNewAndLs(t *testing.T) {
	dir := newTicket(t) // creates PROJ-1 attempt 0001

	if out, code := run(t, "--data", dir, "attempt", "new", "PROJ-1", "--tool", "aider"); code != 0 {
		t.Fatalf("attempt new exited %d: %s", code, out)
	}
	out, code := run(t, "--data", dir, "attempt", "ls", "PROJ-1")
	if code != 0 {
		t.Fatalf("attempt ls exited %d", code)
	}
	if !strings.Contains(out, "0001") || !strings.Contains(out, "0002") {
		t.Errorf("attempt ls missing ids:\n%s", out)
	}
	if !strings.Contains(out, "aider") {
		t.Errorf("attempt ls missing tool:\n%s", out)
	}
}

func TestAttemptFlagTargetsSpecificAttempt(t *testing.T) {
	dir := newTicket(t)
	run(t, "--data", dir, "attempt", "new", "PROJ-1") // 0002

	// Write to 0002 explicitly.
	if _, code := run(t, "--data", dir, "--attempt", "0002", "log", "PROJ-1", "on two", "--type", "note"); code != 0 {
		t.Fatalf("log --attempt 0002 exited %d", code)
	}
	root := store.Root{Dir: dir}

	// 0002 has created + note; 0001 still only created (untouched).
	two, _ := ticketlog.Read(root, "PROJ-1", "0002")
	if len(two) != 2 || two[len(two)-1].Body != "on two" {
		t.Errorf("0002 log wrong: %+v", two)
	}
	one, _ := ticketlog.Read(root, "PROJ-1", "0001")
	if len(one) != 1 {
		t.Errorf("0001 should be untouched, got %d events", len(one))
	}
}

func TestDefaultAttemptIsLatest(t *testing.T) {
	dir := newTicket(t)
	run(t, "--data", dir, "attempt", "new", "PROJ-1") // 0002 becomes latest

	// No --attempt: should land on the latest (0002).
	if _, code := run(t, "--data", dir, "log", "PROJ-1", "defaults to latest", "--type", "note"); code != 0 {
		t.Fatalf("log exited %d", code)
	}
	two, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0002")
	if len(two) != 2 {
		t.Errorf("default attempt was not the latest: 0002 has %d events", len(two))
	}
}
