package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/store"
)

func TestStatusWritesStateAndBoard(t *testing.T) {
	dir := newTicket(t)
	run(t, "--data", dir, "--actor", "agent:x", "escalate", "PROJ-1", "blocked on creds")

	out, code := run(t, "--data", dir, "status")
	if code != 0 {
		t.Fatalf("status exited %d", code)
	}
	if !strings.Contains(out, "Needs me: 1") {
		t.Errorf("board summary wrong: %q", out)
	}
	statePath := store.Root{Dir: dir}.StatePath("PROJ-1", "0001")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state.md not written: %v", err)
	}
	if !strings.Contains(string(data), "state: Needs me") {
		t.Errorf("state.md wrong:\n%s", data)
	}
}

func TestInboxAndMine(t *testing.T) {
	dir := t.TempDir()
	run(t, "--data", dir, "new", "PROJ-1", "--title", "A", "--assignee", "dave")
	run(t, "--data", dir, "new", "PROJ-2", "--title", "B", "--assignee", "sam")
	run(t, "--data", dir, "--actor", "agent:x", "escalate", "PROJ-1", "q1")
	run(t, "--data", dir, "--actor", "agent:x", "escalate", "PROJ-2", "q2")

	all, _ := run(t, "--data", dir, "inbox")
	if !strings.Contains(all, "PROJ-1/0001 #2") || !strings.Contains(all, "PROJ-2/0001 #2") {
		t.Errorf("inbox should list both:\n%s", all)
	}

	mine, _ := run(t, "--data", dir, "--actor", "human:dave", "inbox", "--mine")
	if !strings.Contains(mine, "PROJ-1") || strings.Contains(mine, "PROJ-2") {
		t.Errorf("inbox --mine should show only dave's:\n%s", mine)
	}
}

func TestAuditPassesThenFailsOnTamper(t *testing.T) {
	dir := newTicket(t)
	run(t, "--data", dir, "--actor", "agent:x", "log", "PROJ-1", "a note", "--type", "note")

	if out, code := run(t, "--data", dir, "audit", "PROJ-1"); code != 0 {
		t.Fatalf("audit of clean chain exited %d: %s", code, out)
	}

	// Tamper the genesis event body.
	logDir := store.Root{Dir: dir}.LogDir("PROJ-1", "0001")
	entries, _ := os.ReadDir(logDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "-0001-") {
			p := filepath.Join(logDir, e.Name())
			b, _ := os.ReadFile(p)
			os.WriteFile(p, []byte(strings.Replace(string(b), "Attempt", "Tampered", 1)), 0o644)
		}
	}

	out, code := run(t, "--data", dir, "audit", "PROJ-1")
	if code != ExitAuditFailed {
		t.Fatalf("audit exit = %d want %d\n%s", code, ExitAuditFailed, out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("audit output missing FAIL: %q", out)
	}
}

func TestBriefCommandOutput(t *testing.T) {
	dir := newTicket(t)
	run(t, "--data", dir, "--actor", "agent:x", "log", "PROJ-1", "chose X over Y", "--type", "decision")
	out, code := run(t, "--data", dir, "brief", "PROJ-1")
	if code != 0 {
		t.Fatalf("brief exited %d", code)
	}
	if !strings.Contains(out, "BRIEF PROJ-1") || !strings.Contains(out, "chose X over Y") {
		t.Errorf("brief output unexpected:\n%s", out)
	}
}
