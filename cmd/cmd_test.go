package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// run executes the root command with args and returns combined output and the
// exit code Execute would produce. Package-level flag vars are reset first so
// tests don't contaminate each other.
func run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	dataFlag, actorFlag, attemptFlag = "", "", ""
	logType = ""
	logRefs, logArtefacts, escalateArtefacts = nil, nil, nil
	logURLs, logLinks, reviewURLs, reviewLinks = nil, nil, nil, nil
	newTitle, newProject, newTeam, newAssignee, newSpecFile = "", "", "", "", ""
	newTool, newModel, newRepo, newBase = "", "", "", ""
	attemptTool, attemptModel, attemptFrom, attemptRepo, attemptBase = "", "", "", "", ""
	inboxMine = false
	ctlLogsFollow, ctlLogsJSON = false, false
	ctlStatusAll = false
	mergeDryRun, mergeSync, landEscalate, landNoEscalate = false, false, false, false
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

// newTicket creates a data root and a ticket "PROJ-1", returning the root. The
// repo path is a required attribute now (drvctl-017); the data dir doubles as a
// placeholder since these tests never bring the attempt up.
func newTicket(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, code := run(t, "--data", dir, "--actor", "human:test", "new", "PROJ-1", "--title", "Test", "--repo", dir); code != 0 {
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

// A decision logged against a Review attempt reopens it: Review -> Running,
// with the decision body as the recorded reason. This is the first-class
// Review -> Running transition — no escalate/resolve workaround.
func TestDecisionAfterReviewReopensToRunning(t *testing.T) {
	dir := newTicket(t)
	root := store.Root{Dir: dir}

	if _, code := run(t, "--data", dir, "--actor", "agent:x", "review", "PROJ-1", "claims done"); code != 0 {
		t.Fatalf("review exited %d", code)
	}
	if m, _ := project.LoadAttempt(root, "PROJ-1", "0001"); m.State != project.Review {
		t.Fatalf("state after review = %q want Review", m.State)
	}

	reason := "verification surfaced missing scope; back to work"
	if out, code := run(t, "--data", dir, "--actor", "human:dave", "log", "PROJ-1", reason, "--type", "decision"); code != 0 {
		t.Fatalf("log decision exited %d: %s", code, out)
	}

	m, err := project.LoadAttempt(root, "PROJ-1", "0001")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != project.Running {
		t.Errorf("state after reopening decision = %q want Running", m.State)
	}
	last := m.Events[len(m.Events)-1]
	if last.Type != "decision" || last.Body != reason {
		t.Errorf("reopen reason not recorded on the log: %+v", last)
	}
}

// specTitle returns the title project derives from a written ticket's spec.md,
// the same value the board shows.
func specTitle(t *testing.T, dir, id string) string {
	t.Helper()
	m, err := project.LoadAttempt(store.Root{Dir: dir}, id, "0001")
	if err != nil {
		t.Fatalf("load attempt %s: %v", id, err)
	}
	return m.Title
}

// A title-less import carried by --title yields a titled ticket, and the design
// doc's body survives verbatim.
func TestSpecImportInjectsTitle(t *testing.T) {
	dir := t.TempDir()
	specFile := filepath.Join(dir, "imported.md")
	os.WriteFile(specFile, []byte("# Imported design\n\nbody"), 0o644)
	if _, code := run(t, "--data", dir, "new", "PROJ-2", "--spec", specFile, "--title", "Real Title", "--repo", dir); code != 0 {
		t.Fatalf("new --spec --title exited %d", code)
	}
	got, _ := os.ReadFile(store.Root{Dir: dir}.SpecPath("PROJ-2"))
	if !strings.Contains(string(got), "Imported design") {
		t.Errorf("imported body not preserved:\n%s", got)
	}
	if got := specTitle(t, dir, "PROJ-2"); got != "Real Title" {
		t.Errorf("title = %q, want injected %q", got, "Real Title")
	}
}

// An import whose frontmatter already carries a title needs no --title.
func TestSpecImportUsesFrontmatterTitle(t *testing.T) {
	dir := t.TempDir()
	specFile := filepath.Join(dir, "imported.md")
	os.WriteFile(specFile, []byte("---\ntitle: From Frontmatter\n---\n\nbody"), 0o644)
	if _, code := run(t, "--data", dir, "new", "PROJ-3", "--spec", specFile, "--repo", dir); code != 0 {
		t.Fatalf("new --spec exited %d", code)
	}
	if got := specTitle(t, dir, "PROJ-3"); got != "From Frontmatter" {
		t.Errorf("title = %q, want %q", got, "From Frontmatter")
	}
}

// No title from any source is rejected, and nothing is written.
func TestNewRequiresTitle(t *testing.T) {
	dir := t.TempDir()
	if _, code := run(t, "--data", dir, "new", "PROJ-4"); code == 0 {
		t.Error("expected nonzero exit creating a title-less ticket")
	}
	if (store.Root{Dir: dir}).Exists("PROJ-4") {
		t.Error("rejected new left an orphan ticket dir")
	}

	// A blank/whitespace --title counts as unset.
	if _, code := run(t, "--data", dir, "new", "PROJ-4", "--title", "   "); code == 0 {
		t.Error("expected nonzero exit for a whitespace-only title")
	}

	// A title-less import with no --title is rejected too.
	specFile := filepath.Join(dir, "prose.md")
	os.WriteFile(specFile, []byte("# just prose\n"), 0o644)
	if _, code := run(t, "--data", dir, "new", "PROJ-4", "--spec", specFile); code == 0 {
		t.Error("expected nonzero exit importing a title-less doc without --title")
	}
	if (store.Root{Dir: dir}).Exists("PROJ-4") {
		t.Error("rejected import left an orphan ticket dir")
	}
}

// A title but no --repo is rejected, and nothing is written — the repo is a
// required attribute now (drvctl-017), refused up front with no orphan.
func TestNewRequiresRepo(t *testing.T) {
	dir := t.TempDir()
	if _, code := run(t, "--data", dir, "new", "PROJ-6", "--title", "Has a title"); code == 0 {
		t.Error("expected nonzero exit creating a repo-less ticket")
	}
	if (store.Root{Dir: dir}).Exists("PROJ-6") {
		t.Error("rejected new left an orphan ticket dir")
	}

	// A whitespace-only --repo counts as unset.
	if _, code := run(t, "--data", dir, "new", "PROJ-6", "--title", "Has a title", "--repo", "   "); code == 0 {
		t.Error("expected nonzero exit for a whitespace-only repo")
	}
	if (store.Root{Dir: dir}).Exists("PROJ-6") {
		t.Error("rejected new left an orphan ticket dir")
	}
}

// A title carrying YAML metacharacters (a colon, a quote) must survive
// scaffolding into valid frontmatter and round-trip through the same read path
// the board and brief use. Before the scaffolder quoted the value, an unquoted
// `title: Foo: bar` produced invalid YAML and every later LoadAttempt died with
// "mapping values are not allowed in this context".
func TestNewTitleWithYAMLMetacharsRoundTrips(t *testing.T) {
	cases := map[string]string{
		"PROJ-COLON":  "Fix: the parser",
		"PROJ-DQUOTE": `Handle "quoted" titles`,
		"PROJ-SQUOTE": "It's a colon: really",
		"PROJ-HASH":   "#leading-hash and: colon",
	}
	for id, title := range cases {
		dir := t.TempDir()
		if _, code := run(t, "--data", dir, "new", id, "--title", title, "--repo", dir); code != 0 {
			t.Fatalf("new %s exited %d", id, code)
		}
		// LoadAttempt parses the spec frontmatter — it must not choke, and the
		// title must come back byte-identical.
		if got := specTitle(t, dir, id); got != title {
			t.Errorf("%s title = %q, want %q", id, got, title)
		}
	}
}

// The project/team/assignee scaffold flags are free-text too, so a colon- or
// quote-bearing value must also yield parseable frontmatter that round-trips.
func TestNewFlagFieldsWithYAMLMetacharsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	code := 0
	_, code = run(t, "--data", dir, "new", "PROJ-FLAGS", "--title", "Plain", "--repo", dir,
		"--project", "Team: A", "--team", `He said "go"`, "--assignee", "a:b")
	if code != 0 {
		t.Fatalf("new exited %d", code)
	}
	// An unquoted `project: Team: A` line would make the whole frontmatter block
	// invalid, so LoadAttempt parsing at all proves every field encoded safely;
	// Assignee (the one flag field on Attempt) additionally checks the round-trip.
	m, err := project.LoadAttempt(store.Root{Dir: dir}, "PROJ-FLAGS", "0001")
	if err != nil {
		t.Fatalf("LoadAttempt choked on flag metacharacters: %v", err)
	}
	if m.Assignee != "a:b" {
		t.Errorf("assignee = %q, want %q", m.Assignee, "a:b")
	}
}

// The `title` command shares injectTitle with `new --spec`, so a colon- or
// quote-bearing retitle must also produce parseable frontmatter.
func TestTitleCommandWithYAMLMetacharsRoundTrips(t *testing.T) {
	dir := newTicket(t)
	want := `Rename to "Board": v2`
	if _, code := run(t, "--data", dir, "title", "PROJ-1", want); code != 0 {
		t.Fatalf("title exited %d", code)
	}
	if got := specTitle(t, dir, "PROJ-1"); got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
}

// Supplying a title from both sources is ambiguous and rejected.
func TestNewRejectsDoubleTitle(t *testing.T) {
	dir := t.TempDir()
	specFile := filepath.Join(dir, "imported.md")
	os.WriteFile(specFile, []byte("---\ntitle: From Frontmatter\n---\n\nbody"), 0o644)
	if _, code := run(t, "--data", dir, "new", "PROJ-5", "--spec", specFile, "--title", "From Flag"); code == 0 {
		t.Error("expected nonzero exit when title is set by both sources")
	}
}
