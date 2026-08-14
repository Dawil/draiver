package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/session"
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

// TestStatusPendingCount drives the Pending projection end to end: an enabled
// attempt with no live agent counts as Pending; giving it a live session flips it
// to Running; an escalation makes it Needs-me — never Pending.
func TestStatusPendingCount(t *testing.T) {
	dir := newTicket(t) // PROJ-1/0001 — Running, disabled by default
	root := store.Root{Dir: dir}

	// Disabled + Running + no agent is plain Running, not Pending: being in Running
	// is not consent to supervise.
	out, code := run(t, "--data", dir, "status")
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	if !strings.Contains(out, "Running: 1") || !strings.Contains(out, "Pending: 0") {
		t.Errorf("disabled Running should be Running, not Pending:\n%s", out)
	}

	// Enable it: now enabled + Running + no live agent = Pending (the enabled-via-
	// parent-but-not-yet-admitted case).
	if _, code := run(t, "--data", dir, "--actor", "human:test", "ctl", "enable", "PROJ-1@0001"); code != 0 {
		t.Fatal("enable failed")
	}
	out, _ = run(t, "--data", dir, "status")
	if !strings.Contains(out, "Running: 0") || !strings.Contains(out, "Pending: 1") {
		t.Errorf("enabled Running with no agent should be Pending:\n%s", out)
	}

	// A live agent session flips it back to Running. Record session.json with this
	// test process's pid — a definitely-live process.
	s, err := session.Open(root, "PROJ-1", "0001")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteIdentity(session.Identity{Adapter: "claude-code", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	out, _ = run(t, "--data", dir, "status")
	if !strings.Contains(out, "Running: 1") || !strings.Contains(out, "Pending: 0") {
		t.Errorf("a live agent should be Running, not Pending:\n%s", out)
	}

	// An escalation outranks everything: Needs-me, never Pending — even though the
	// live pid is now gone (identity still on disk, but we escalate, and an open
	// escalation is Needs-me by the log alone).
	run(t, "--data", dir, "--actor", "agent:x", "escalate", "PROJ-1", "blocked") // exits 3 by protocol
	out, _ = run(t, "--data", dir, "status")
	if !strings.Contains(out, "Needs me: 1") || !strings.Contains(out, "Pending: 0") {
		t.Errorf("an escalated attempt should be Needs-me, not Pending:\n%s", out)
	}
}

// TestStatusPendingViaParent is acceptance #1 end to end: a child pulled into the
// fleet only because an enabled parent `wants:` it — with no `enable` event of its
// own — still derives Pending in `status`.
func TestStatusPendingViaParent(t *testing.T) {
	dir := t.TempDir()
	run(t, "--data", dir, "--actor", "human:test", "new", "CAP", "--title", "Cap", "--repo", dir)
	run(t, "--data", dir, "--actor", "human:test", "new", "CHILD", "--title", "Child", "--repo", dir)
	run(t, "--data", dir, "--actor", "human:test", "depends", "CAP", "--wants", "CHILD")
	// Enable only the parent. The child has no enable event of its own.
	if _, code := run(t, "--data", dir, "--actor", "human:test", "ctl", "enable", "CAP@0001"); code != 0 {
		t.Fatal("enable CAP failed")
	}

	out, code := run(t, "--data", dir, "status")
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	// Both the enabled parent and its via-parent child are desired, Running, and
	// have no live agent: Pending: 2.
	if !strings.Contains(out, "Pending: 2") || !strings.Contains(out, "Running: 0") {
		t.Errorf("enabled parent + via-parent child should both be Pending:\n%s", out)
	}
}

func TestInboxAndMine(t *testing.T) {
	dir := t.TempDir()
	run(t, "--data", dir, "new", "PROJ-1", "--title", "A", "--assignee", "dave", "--repo", dir)
	run(t, "--data", dir, "new", "PROJ-2", "--title", "B", "--assignee", "sam", "--repo", dir)
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
