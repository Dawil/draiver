package cmd

import (
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func TestAttemptNewAndLs(t *testing.T) {
	dir := newTicket(t) // creates PROJ-1 attempt 0001

	if out, code := run(t, "--data", dir, "attempt", "new", "PROJ-1", "--tool", "aider", "--repo", dir); code != 0 {
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
	run(t, "--data", dir, "attempt", "new", "PROJ-1", "--repo", dir) // 0002

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

// --repo is required on attempt new unless --from supplies it by inheritance
// (drvctl-017 keeping drvctl-015's inheritance). No --repo and no --from → refused.
func TestAttemptNewRequiresRepo(t *testing.T) {
	dir := newTicket(t) // PROJ-1/0001 recorded a repo
	if out, code := run(t, "--data", dir, "attempt", "new", "PROJ-1"); code == 0 {
		t.Fatalf("expected nonzero exit for a repo-less attempt; out=%s", out)
	}
	// Only 0001 exists; the refused attempt wrote nothing.
	if ids, _ := (store.Root{Dir: dir}).ListAttempts("PROJ-1"); len(ids) != 1 {
		t.Errorf("refused attempt left an id behind: %v", ids)
	}
}

// --from inherits the parent's repo when --repo is omitted, so the sibling
// targets the same working tree without re-specifying it.
func TestAttemptNewInheritsRepoFromParent(t *testing.T) {
	dir := newTicket(t) // PROJ-1/0001 recorded repo == dir
	if out, code := run(t, "--data", dir, "attempt", "new", "PROJ-1", "--from", "0001"); code != 0 {
		t.Fatalf("attempt new --from exited %d: %s", code, out)
	}
	m, err := project.LoadAttempt(store.Root{Dir: dir}, "PROJ-1", "0002")
	if err != nil {
		t.Fatal(err)
	}
	if m.Repo != dir {
		t.Errorf("inherited repo = %q, want %q", m.Repo, dir)
	}
}

func TestDefaultAttemptIsLatest(t *testing.T) {
	dir := newTicket(t)
	run(t, "--data", dir, "attempt", "new", "PROJ-1", "--repo", dir) // 0002 becomes latest

	// No --attempt: should land on the latest (0002).
	if _, code := run(t, "--data", dir, "log", "PROJ-1", "defaults to latest", "--type", "note"); code != 0 {
		t.Fatalf("log exited %d", code)
	}
	two, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0002")
	if len(two) != 2 {
		t.Errorf("default attempt was not the latest: 0002 has %d events", len(two))
	}
}
