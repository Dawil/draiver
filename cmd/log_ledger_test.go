package cmd

import (
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// TestLogAcceptsTicketAttemptForm is the write side of the codebase-ledger epic
// (drv-038): `log` now resolves the `ticket@attempt` address form (symmetric with
// `recall`), so an agent can record findings into its own scoped attempt on a
// ledger ticket — `draiver log LEDGER@0002 …` — rather than being pinned to the
// ticket's latest attempt.
func TestLogAcceptsTicketAttemptForm(t *testing.T) {
	dir := t.TempDir()
	// A codebase ledger with its defining attempt 0001.
	if _, code := run(t, "--data", dir, "--actor", "human:test", "new", "LEDGER", "--title", "Codebase", "--repo", dir, "--type", "codebase"); code != 0 {
		t.Fatalf("new ledger exited %d", code)
	}
	// A second, scoped perspective attempt 0002.
	if _, code := run(t, "--data", dir, "--actor", "agent:x", "attempt", "new", "LEDGER", "--repo", dir); code != 0 {
		t.Fatalf("attempt new exited %d", code)
	}

	// Address the explicit attempt: the fact must land on 0002, not the latest.
	out, code := run(t, "--data", dir, "--actor", "agent:x", "log", "LEDGER@0002", "shared build gotcha", "--type", "gotcha", "--tag", "build")
	if code != 0 {
		t.Fatalf("log LEDGER@0002 exited %d: %s", code, out)
	}
	if !strings.Contains(out, "on LEDGER/0002") {
		t.Errorf("log output did not confirm the 0002 target: %q", out)
	}

	root := store.Root{Dir: dir}
	ev2, _ := ticketlog.Read(root, "LEDGER", "0002")
	last := ev2[len(ev2)-1]
	if last.Type != "gotcha" || last.Body != "shared build gotcha" || len(last.Tags) != 1 || last.Tags[0] != "build" {
		t.Errorf("event on 0002 wrong: %+v", last)
	}
	// 0001 must be untouched (only its created event).
	ev1, _ := ticketlog.Read(root, "LEDGER", "0001")
	if len(ev1) != 1 {
		t.Errorf("attempt 0001 should be untouched, has %d events", len(ev1))
	}

	// A bad attempt in the address form is rejected, not silently redirected.
	if _, code := run(t, "--data", dir, "--actor", "agent:x", "log", "LEDGER@9999", "x", "--type", "note"); code == 0 {
		t.Error("log to a nonexistent attempt should fail")
	}
}
