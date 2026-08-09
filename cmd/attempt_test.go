package cmd

import (
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/attempt"
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

// attempt set writes provenance into an existing attempt.md and appends NO log
// event (metadata, outside the hash-chained log — the `title` precedent). Only
// the flags passed change; an omitted flag leaves its field untouched.
func TestAttemptSetWritesProvenanceOutsideLog(t *testing.T) {
	dir := newTicket(t) // PROJ-1/0001, repo == dir, base defaulted
	root := store.Root{Dir: dir}
	before, _ := ticketlog.Read(root, "PROJ-1", "0001")

	if out, code := run(t, "--data", dir, "attempt", "set", "PROJ-1", "--base", "release/v2", "--tool", "aider"); code != 0 {
		t.Fatalf("attempt set exited %d: %s", code, out)
	}
	m, err := project.LoadAttempt(root, "PROJ-1", "0001")
	if err != nil {
		t.Fatal(err)
	}
	if m.Base != "release/v2" || m.Tool != "aider" {
		t.Errorf("set fields not persisted: base=%q tool=%q", m.Base, m.Tool)
	}
	if m.Repo != dir {
		t.Errorf("an omitted --repo must leave repo untouched, got %q", m.Repo)
	}
	// No log event was appended: audit is untouched.
	after, _ := ticketlog.Read(root, "PROJ-1", "0001")
	if len(after) != len(before) {
		t.Errorf("attempt set must not append to the log: %d -> %d events", len(before), len(after))
	}
}

// A targeted setter: setting one field never blanks another.
func TestAttemptSetIsTargeted(t *testing.T) {
	dir := newTicket(t)
	root := store.Root{Dir: dir}
	if _, code := run(t, "--data", dir, "attempt", "set", "PROJ-1", "--base", "hotfix"); code != 0 {
		t.Fatal("set --base exited nonzero")
	}
	if _, code := run(t, "--data", dir, "attempt", "set", "PROJ-1", "--tool", "codex"); code != 0 {
		t.Fatal("set --tool exited nonzero")
	}
	m, _ := project.LoadAttempt(root, "PROJ-1", "0001")
	if m.Base != "hotfix" || m.Tool != "codex" || m.Repo != dir {
		t.Errorf("targeted set clobbered a sibling field: %+v", m)
	}
}

func TestAttemptSetValidation(t *testing.T) {
	dir := newTicket(t)
	// No flags: nothing to set.
	if _, code := run(t, "--data", dir, "attempt", "set", "PROJ-1"); code == 0 {
		t.Error("expected nonzero exit with no flags")
	}
	// Explicit empty --repo is refused (required-if-given).
	if _, code := run(t, "--data", dir, "attempt", "set", "PROJ-1", "--repo", "   "); code == 0 {
		t.Error("expected nonzero exit for a whitespace-only --repo")
	}
	// Unknown ticket.
	if _, code := run(t, "--data", dir, "attempt", "set", "NOPE-9", "--tool", "x"); code == 0 {
		t.Error("expected nonzero exit for an unknown ticket")
	}
}

// A base-less attempt is exactly what `ctl merge` refuses; `attempt set --base`
// is the fix. This is the wedged-attempt recovery the ticket exists to enable.
func TestAttemptSetUnwedgesMerge(t *testing.T) {
	dir := newTicket(t)
	root := store.Root{Dir: dir}
	// Blank the base to simulate a legacy/hand-made attempt that records none.
	m, _ := attempt.LoadMeta(root, "PROJ-1", "0001")
	m.Base = ""
	if err := attempt.WriteMeta(root, m); err != nil {
		t.Fatal(err)
	}
	// ctl merge refuses a base-less attempt (rootCmd silences the error text, so
	// assert on the nonzero exit; loadLandContext's message is covered elsewhere).
	if _, code := run(t, "--data", dir, "ctl", "merge", "PROJ-1"); code == 0 {
		t.Fatal("expected ctl merge to refuse a base-less attempt")
	}
	// attempt set --base fills it in; the field is now readable.
	if _, code := run(t, "--data", dir, "attempt", "set", "PROJ-1", "--base", "main"); code != 0 {
		t.Fatal("attempt set --base exited nonzero")
	}
	if got, _ := project.LoadAttempt(root, "PROJ-1", "0001"); got.Base != "main" {
		t.Errorf("base not set after attempt set: %q", got.Base)
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
