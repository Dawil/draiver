package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// gitInDir runs a git command in dir with a fixed identity, failing on error.
func gitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// pyramidCheckout makes a git checkout on branch draiver/<ticket>/<attempt> with a
// .test-pyramid.yaml built from name→run rungs and chdirs the test into it. The
// pyramid file is left uncommitted (a dirty tree) unless commit is true — the
// distinction --log turns on. It returns the checkout path.
func pyramidCheckout(t *testing.T, ticket, attempt string, commit bool, rungs [][2]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitInDir(t, dir, "init", "-q", "-b", "main")
	gitInDir(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	gitInDir(t, dir, "checkout", "-q", "-b", "draiver/"+ticket+"/"+attempt)

	var b strings.Builder
	b.WriteString("levels:\n")
	for _, r := range rungs {
		fmt.Fprintf(&b, "  - name: %s\n    run: %s\n", r[0], r[1])
	}
	if err := os.WriteFile(filepath.Join(dir, ".test-pyramid.yaml"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if commit {
		gitInDir(t, dir, "add", ".test-pyramid.yaml")
		gitInDir(t, dir, "commit", "-q", "-m", "add pyramid")
	}
	t.Chdir(dir)
	return dir
}

// pyramidCheckoutYAML is pyramidCheckout's sibling for tests that need the richer
// environments schema: it writes body verbatim as the checkout's .test-pyramid.yaml
// (committed when commit is true) on the attempt branch and chdirs into it.
func pyramidCheckoutYAML(t *testing.T, ticket, attempt string, commit bool, body string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitInDir(t, dir, "init", "-q", "-b", "main")
	gitInDir(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	gitInDir(t, dir, "checkout", "-q", "-b", "draiver/"+ticket+"/"+attempt)
	if err := os.WriteFile(filepath.Join(dir, ".test-pyramid.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if commit {
		gitInDir(t, dir, "add", ".test-pyramid.yaml")
		gitInDir(t, dir, "commit", "-q", "-m", "add pyramid")
	}
	t.Chdir(dir)
	return dir
}

// A rung with an environment runs up → healthchecks → run → down, in that order,
// and down runs after the rung's run. Each step echoes a marker so the output
// ordering is observable.
func TestEnvRunsUpHealthcheckRunDownInOrder(t *testing.T) {
	body := `
environments:
  - name: local
    up: echo MARK_UP
    down: echo MARK_DOWN
    healthchecks:
      - name: ready
        script: echo MARK_HC
levels:
  - name: unit
    run: echo MARK_RUN
    environment: local
`
	pyramidCheckoutYAML(t, "PROJ-1", "0001", false, body)
	out, code := run(t, "test")
	if code != 0 {
		t.Fatalf("all-green env rung exited %d: %s", code, out)
	}
	order := []string{"MARK_UP", "MARK_HC", "MARK_RUN", "MARK_DOWN"}
	last := -1
	for _, m := range order {
		i := strings.Index(out, m)
		if i < 0 {
			t.Fatalf("missing %q in output:\n%s", m, out)
		}
		if i < last {
			t.Errorf("step %q out of order in output:\n%s", m, out)
		}
		last = i
	}
}

// A failing env `up` is an infrastructure fault: exit ExitEnvUp, the rung's run is
// never reached, and down still runs (best-effort teardown of a partial up).
func TestEnvUpFailureEscalates(t *testing.T) {
	body := `
environments:
  - name: local
    up: "false"
    down: echo MARK_DOWN
levels:
  - name: unit
    run: echo MARK_RUN
    environment: local
`
	pyramidCheckoutYAML(t, "PROJ-1", "0001", false, body)
	out, code := run(t, "test")
	if code != ExitEnvUp {
		t.Fatalf("up failure should exit %d, got %d: %s", ExitEnvUp, code, out)
	}
	if strings.Contains(out, "MARK_RUN") {
		t.Errorf("run must not execute after a failed up: %s", out)
	}
	if !strings.Contains(out, "MARK_DOWN") {
		t.Errorf("down must run even after a failed up: %s", out)
	}
}

// A red healthcheck is a dependency-not-ready block: exit ExitBlocked, run never
// executes, down still runs.
func TestEnvHealthcheckRedBlocks(t *testing.T) {
	body := `
environments:
  - name: local
    down: echo MARK_DOWN
    healthchecks:
      - name: dep
        script: "false"
levels:
  - name: unit
    run: echo MARK_RUN
    environment: local
`
	pyramidCheckoutYAML(t, "PROJ-1", "0001", false, body)
	out, code := run(t, "test")
	if code != ExitBlocked {
		t.Fatalf("red healthcheck should exit %d, got %d: %s", ExitBlocked, code, out)
	}
	if strings.Contains(out, "MARK_RUN") {
		t.Errorf("run must not execute after a red healthcheck: %s", out)
	}
	if !strings.Contains(out, "MARK_DOWN") {
		t.Errorf("down must run after a red healthcheck: %s", out)
	}
}

// A failing `run` inside an environment is a code fault: plain exit 1 (not an env
// exit code), and down still runs.
func TestEnvRunFailureIsCodeFault(t *testing.T) {
	body := `
environments:
  - name: local
    down: echo MARK_DOWN
levels:
  - name: unit
    run: "false"
    environment: local
`
	pyramidCheckoutYAML(t, "PROJ-1", "0001", false, body)
	out, code := run(t, "test")
	if code != 1 {
		t.Fatalf("a failing run should be a code fault (exit 1), got %d: %s", code, out)
	}
	if !strings.Contains(out, "MARK_DOWN") {
		t.Errorf("down must run after a failing run: %s", out)
	}
}

// A failing `down` is best-effort: it never changes the rung's verdict, so an
// otherwise-green rung still exits 0 and a warning is surfaced.
func TestEnvDownFailureIsBestEffort(t *testing.T) {
	body := `
environments:
  - name: local
    down: "false"
levels:
  - name: unit
    run: echo MARK_RUN
    environment: local
`
	pyramidCheckoutYAML(t, "PROJ-1", "0001", false, body)
	out, code := run(t, "test")
	if code != 0 {
		t.Fatalf("a failing down must not change a green verdict, got %d: %s", code, out)
	}
	if !strings.Contains(out, "warning") || !strings.Contains(out, "down failed") {
		t.Errorf("a failing down should surface a warning: %s", out)
	}
}

// A repo with no .test-pyramid.yaml has nothing to run: exit 0 with a clear note.
func TestBareNoPyramidIsClean(t *testing.T) {
	t.Chdir(t.TempDir())
	out, code := run(t, "test")
	if code != 0 {
		t.Fatalf("test exited %d: %s", code, out)
	}
	if !strings.Contains(out, "nothing to test") {
		t.Errorf("missing empty-pyramid note: %q", out)
	}
}

// Bare test climbs the whole pyramid and reports all-green, touching no log.
func TestBareClimbsToTop(t *testing.T) {
	pyramidCheckout(t, "PROJ-1", "0001", false, [][2]string{{"unit", "true"}, {"integration", "true"}})
	out, code := run(t, "test")
	if code != 0 {
		t.Fatalf("test exited %d: %s", code, out)
	}
	if !strings.Contains(out, `all green through "integration"`) {
		t.Errorf("did not report climbing to the top rung: %q", out)
	}
}

// Bare test stops at the first non-green rung and exits nonzero; later rungs never run.
func TestBareStopsAtFirstFailure(t *testing.T) {
	pyramidCheckout(t, "PROJ-1", "0001", false, [][2]string{{"unit", "false"}, {"integration", "true"}})
	out, code := run(t, "test")
	if code == 0 {
		t.Fatalf("expected nonzero exit on a failing rung; out: %s", out)
	}
	if !strings.Contains(out, "== unit") {
		t.Errorf("did not run the base rung: %q", out)
	}
	if strings.Contains(out, "== integration") {
		t.Errorf("kept climbing past a non-green rung: %q", out)
	}
}

// A RUNG argument caps the climb: rungs above it never run.
func TestRungArgCapsTheClimb(t *testing.T) {
	pyramidCheckout(t, "PROJ-1", "0001", false, [][2]string{{"unit", "true"}, {"integration", "false"}})
	out, code := run(t, "test", "unit")
	if code != 0 {
		t.Fatalf("test unit exited %d: %s", code, out)
	}
	if !strings.Contains(out, `all green through "unit"`) {
		t.Errorf("did not stop at the requested rung: %q", out)
	}
	if strings.Contains(out, "== integration") {
		t.Errorf("climbed past the requested rung: %q", out)
	}
}

// An unknown RUNG is rejected, naming the declared rungs.
func TestUnknownRungRejected(t *testing.T) {
	pyramidCheckout(t, "PROJ-1", "0001", false, [][2]string{{"unit", "true"}})
	out, code := run(t, "test", "nope")
	if code == 0 {
		t.Fatalf("expected nonzero exit for an unknown rung; out: %s", out)
	}
	if strings.Contains(out, "== ") {
		t.Errorf("an unknown rung must be rejected before running anything: %q", out)
	}
}

// --log refuses a dirty tree up front and records nothing.
func TestLogRefusesDirtyTree(t *testing.T) {
	dir := newTicket(t)                                                        // store root with PROJ-1/0001
	pyramidCheckout(t, "PROJ-1", "0001", false, [][2]string{{"unit", "true"}}) // uncommitted pyramid → dirty
	out, code := run(t, "--data", dir, "test", "--log")
	if code == 0 {
		t.Fatalf("expected nonzero exit on a dirty tree; out: %s", out)
	}
	if strings.Contains(out, "== ") {
		t.Errorf("--log must refuse a dirty tree before running anything: %q", out)
	}
	events, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	if last := events[len(events)-1]; last.Type == "test-result" {
		t.Error("a refused --log wrote a test-result event")
	}
}

// --log on all-green through the requested rung appends exactly one test-result
// event carrying the rung and the exact HEAD commit, attributed to the actor.
func TestLogRecordsGreenResult(t *testing.T) {
	dir := newTicket(t)
	checkout := pyramidCheckout(t, "PROJ-1", "0001", true, [][2]string{{"unit", "true"}, {"integration", "true"}})
	head := headOf(t, checkout, "HEAD")

	before, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	out, code := run(t, "--data", dir, "--actor", "agent:x", "test", "--log")
	if code != 0 {
		t.Fatalf("test --log exited %d: %s", code, out)
	}
	if !strings.Contains(out, "recorded test-result") {
		t.Errorf("missing record confirmation: %q", out)
	}

	after, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	if len(after) != len(before)+1 {
		t.Fatalf("expected exactly one new event, got %d→%d", len(before), len(after))
	}
	last := after[len(after)-1]
	if last.Type != "test-result" {
		t.Fatalf("last event type = %q, want test-result", last.Type)
	}
	if last.Rung != "integration" {
		t.Errorf("Rung = %q, want integration", last.Rung)
	}
	if last.Commit != head {
		t.Errorf("Commit = %q, want HEAD %q", last.Commit, head)
	}
	if last.Actor != "agent:x" {
		t.Errorf("Actor = %q, want agent:x", last.Actor)
	}
}

// A failing --log run records nothing and exits nonzero: the log holds only
// passing results by construction.
func TestLogFailingRunRecordsNothing(t *testing.T) {
	dir := newTicket(t)
	pyramidCheckout(t, "PROJ-1", "0001", true, [][2]string{{"unit", "false"}})

	before, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	out, code := run(t, "--data", dir, "test", "--log")
	if code == 0 {
		t.Fatalf("expected nonzero exit on a failing --log run; out: %s", out)
	}
	after, _ := ticketlog.Read(store.Root{Dir: dir}, "PROJ-1", "0001")
	if len(after) != len(before) {
		t.Errorf("a failing --log run wrote an event: %d→%d", len(before), len(after))
	}
}
