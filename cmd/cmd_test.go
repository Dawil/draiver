package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"draiver/internal/store"
	"draiver/internal/ticketlog"
)

// run executes the root command with args and returns combined output and the
// exit code Execute would produce. Package-level flag vars are reset first so
// tests don't contaminate each other.
func run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	dataFlag, actorFlag, attemptFlag = "", "", ""
	logType = ""
	logRefs, logArtefacts, escalateArtefacts = nil, nil, nil
	newTitle, newProject, newTeam, newAssignee, newSpecFile = "", "", "", "", ""
	newTool, newModel = "", ""
	attemptTool, attemptModel, attemptFrom = "", "", ""
	inboxMine = false
	t.Setenv("DRAIVER_ATTEMPT", "")

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(args)

	err := rootCmd.Execute()
	code := 0
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			code = ee.code
		} else {
			code = 1
		}
	}
	return out.String(), code
}

// newTicket creates a data root and a ticket "PROJ-1", returning the root.
func newTicket(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, code := run(t, "--data", dir, "--actor", "human:test", "new", "PROJ-1", "--title", "Test"); code != 0 {
		t.Fatalf("new exited %d", code)
	}
	return dir
}

func TestNewScaffolds(t *testing.T) {
	dir := newTicket(t)
	root := store.Root{Dir: dir}

	spec, err := os.ReadFile(root.SpecPath("PROJ-1"))
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if !strings.Contains(string(spec), "id: PROJ-1") || !strings.Contains(string(spec), "title: Test") {
		t.Errorf("spec missing identity frontmatter:\n%s", spec)
	}

	events, err := ticketlog.Read(root, "PROJ-1", "0001")
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(events) != 1 || events[0].Type != "created" {
		t.Fatalf("expected one created event, got %+v", events)
	}

	// A second new on the same id must refuse.
	if _, code := run(t, "--data", dir, "--actor", "human:test", "new", "PROJ-1"); code == 0 {
		t.Error("expected nonzero exit re-creating an existing ticket")
	}
}

func TestLogAppendsTypedEvent(t *testing.T) {
	dir := newTicket(t)
	out, code := run(t, "--data", dir, "--actor", "agent:x", "log", "PROJ-1", "cgo needed", "--type", "gotcha")
	if code != 0 {
		t.Fatalf("log exited %d: %s", code, out)
	}
	events, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	last := events[len(events)-1]
	if last.Type != "gotcha" || last.Body != "cgo needed" || last.Actor != "agent:x" {
		t.Errorf("unexpected event: %+v", last)
	}
}

func TestLogRequiresType(t *testing.T) {
	dir := newTicket(t)
	if _, code := run(t, "--data", dir, "log", "PROJ-1", "msg"); code == 0 {
		t.Error("expected nonzero exit without --type")
	}
}

func TestEscalateExitsWithGateCode(t *testing.T) {
	dir := newTicket(t)
	out, code := run(t, "--data", dir, "--actor", "agent:x", "escalate", "PROJ-1", "need prod creds")
	if code != ExitEscalated {
		t.Fatalf("escalate exit = %d want %d", code, ExitEscalated)
	}
	if !strings.Contains(out, "halting") {
		t.Errorf("escalate output missing halt notice: %q", out)
	}
	events, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	last := events[len(events)-1]
	if last.Type != "escalation" {
		t.Errorf("last event = %q want escalation", last.Type)
	}
}

func TestResolveLinksToEscalation(t *testing.T) {
	dir := newTicket(t)
	run(t, "--data", dir, "--actor", "agent:x", "escalate", "PROJ-1", "which base image?")
	// escalation is seq 2 (created=1).
	out, code := run(t, "--data", dir, "--actor", "human:dave", "resolve", "PROJ-1", "2", "use bookworm")
	if code != 0 {
		t.Fatalf("resolve exited %d: %s", code, out)
	}
	events, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	last := events[len(events)-1]
	if last.Type != "resolution" || len(last.Refs) != 1 || last.Refs[0] != 2 {
		t.Errorf("resolution not linked: %+v", last)
	}
}

func TestResolveRejectsNonEscalation(t *testing.T) {
	dir := newTicket(t)
	// seq 1 is the created event, not an escalation.
	if _, code := run(t, "--data", dir, "resolve", "PROJ-1", "1", "nope"); code == 0 {
		t.Error("expected nonzero exit resolving a non-escalation event")
	}
	if _, code := run(t, "--data", dir, "resolve", "PROJ-1", "99", "nope"); code == 0 {
		t.Error("expected nonzero exit resolving a missing seq")
	}
}

func TestReviewAndDone(t *testing.T) {
	dir := newTicket(t)
	run(t, "--data", dir, "--actor", "agent:x", "review", "PROJ-1")
	run(t, "--data", dir, "--actor", "human:dave", "done", "PROJ-1")
	events, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	if events[len(events)-2].Type != "review" || events[len(events)-1].Type != "done" {
		t.Errorf("expected review then done, got %q %q",
			events[len(events)-2].Type, events[len(events)-1].Type)
	}
}

func TestSpecImport(t *testing.T) {
	dir := t.TempDir()
	specFile := filepath.Join(dir, "imported.md")
	os.WriteFile(specFile, []byte("# Imported design\n\nbody"), 0o644)
	if _, code := run(t, "--data", dir, "new", "PROJ-2", "--spec", specFile); code != 0 {
		t.Fatalf("new --spec exited %d", code)
	}
	got, _ := os.ReadFile(store.Root{Dir: dir}.SpecPath("PROJ-2"))
	if !strings.Contains(string(got), "Imported design") {
		t.Errorf("imported spec not used: %s", got)
	}
}
