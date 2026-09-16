//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// DataRoot is a seeded, self-contained draiver data-root "volume": a fresh
// directory tree (spec.md / attempts / hash-chained log) built by a Fixture via
// the real CLI. One is created per scenario under t.TempDir(), so each test runs
// against a known board state and none leaks into the next — reproducibility is
// the point.
type DataRoot struct {
	Dir string // absolute path to the seeded data root
	bin string // draiver binary used to seed and to serve
	t   *testing.T
}

// Fixture is a named seed recipe run against a data root. It receives a live
// Draiver runner already pinned to the target --data dir and a git working tree
// its tickets can point --repo at (attempt creation requires a real tree; the
// board only reads the log, so no session is ever cut against it).
type Fixture struct {
	Name string
	Seed func(d *Draiver, repo *GitRepo)
}

// NewDataRoot seeds a fresh data root from the named fixture and returns a handle.
// The directory lives under t.TempDir(), so the test runner reclaims it.
func NewDataRoot(t *testing.T, f Fixture) *DataRoot {
	t.Helper()
	d := NewDraiver(t)
	// A minimal one-commit repo is enough for board scenarios: `new`/`attempt new`
	// need a real git tree to derive a base branch from, but the board reads only
	// the log.
	repo := NewGitRepo(t, SeedSingleCommit)
	f.Seed(d, repo)
	return &DataRoot{Dir: d.dataDir, bin: d.bin, t: t}
}

// NewDraiver builds (once) the binary and returns a CLI runner pinned to a fresh
// data root under t.TempDir(), attributed to an agent actor by default. Use it
// directly to compose a bespoke scenario (e.g. the forge round-trip) instead of a
// named Fixture.
func NewDraiver(t *testing.T) *Draiver {
	t.Helper()
	return &Draiver{t: t, bin: DraiverBin(t), dataDir: t.TempDir(), actor: "agent:claude-code"}
}

// Dir is the data root this runner writes to.
func (d *Draiver) Dir() string { return d.dataDir }

// SetActor overrides the DRAIVER_ACTOR stamped on writes — e.g. "human:test" to
// exercise the human failure disposition (plain error, not escalate).
func (d *Draiver) SetActor(actor string) { d.actor = actor }

// DataRootHandle wraps this runner's data root so it can be served by the webui
// harness without re-seeding.
func (d *Draiver) DataRootHandle() *DataRoot {
	return &DataRoot{Dir: d.dataDir, bin: d.bin, t: d.t}
}

// Draiver runs the draiver CLI against a fixed --data root with a fixed actor,
// mirroring e2e/global-setup.ts. It is the seeding primitive fixtures compose.
type Draiver struct {
	t       *testing.T
	bin     string
	dataDir string
	actor   string
}

// Run executes `draiver --data <dir> <args...>` with DRAIVER_ACTOR set, failing
// the test on a nonzero exit. It does NOT tolerate escalate's exit 3 — use
// RunEscalate for the one verb that halts by design.
func (d *Draiver) Run(args ...string) string {
	d.t.Helper()
	out, code, err := d.run(args...)
	if err != nil || code != 0 {
		d.t.Fatalf("draiver %s failed (exit %d): %v\n%s", strings.Join(args, " "), code, err, out)
	}
	return out
}

// RunEscalate runs `draiver escalate ...`, which appends the escalation then
// halts with exit 3 by design (drv escalate gate). Exit 3 is the expected
// success signal here, exactly as global-setup.ts swallows it.
func (d *Draiver) RunEscalate(ticket, question string) string {
	d.t.Helper()
	out, code, err := d.run("escalate", ticket, question)
	if err != nil && code != 3 {
		d.t.Fatalf("draiver escalate %s failed (exit %d): %v\n%s", ticket, code, err, out)
	}
	return out
}

// RunAllowFail runs the CLI and returns its combined output and exit code
// without failing the test — for the cases whose expected outcome is a refusal
// (a nonzero exit and nothing recorded).
func (d *Draiver) RunAllowFail(args ...string) (string, int) {
	d.t.Helper()
	out, code, err := d.run(args...)
	if err != nil {
		d.t.Fatalf("draiver %s could not run: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out, code
}

// HasEvent reports whether the attempt's hash-chained log holds an event of the
// given type. Event files are named <ts>-<seq>-<type>.md, so a suffix match is a
// robust, format-independent state assertion (e.g. a `done` event means the
// attempt reached Done).
func (d *Draiver) HasEvent(ticket, attempt, typ string) bool {
	d.t.Helper()
	logDir := filepath.Join(d.dataDir, ticket, "attempts", attempt, "log")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		d.t.Fatalf("read log dir %s: %v", logDir, err)
	}
	suffix := "-" + typ + ".md"
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), suffix) {
			return true
		}
	}
	return false
}

func (d *Draiver) run(args ...string) (string, int, error) {
	full := append([]string{"--data", d.dataDir}, args...)
	cmd := exec.Command(d.bin, full...)
	cmd.Env = append(os.Environ(), "DRAIVER_ACTOR="+d.actor, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			err = nil // an exit code is a result, not a runner failure
		}
	}
	return string(out), code, err
}

// --- Named fixtures --------------------------------------------------------
//
// These reuse the seed shapes proven by e2e/global-setup.ts, adapted to the
// board states the spec calls out (empty-board, mid-escalation,
// review-ready-to-merge).

// EmptyBoard is a data root with no tickets — the cold-start state.
var EmptyBoard = Fixture{
	Name: "empty-board",
	Seed: func(d *Draiver, repo *GitRepo) {},
}

// MidEscalation is a data root whose one ticket has two attempts: 0001 blocked on
// an open escalation (→ Stuck) and 0002 freshly running (→ Running), so the same
// ticket shows twice and the Stuck column is populated.
var MidEscalation = Fixture{
	Name: "mid-escalation",
	Seed: func(d *Draiver, repo *GitRepo) {
		d.Run("new", "PROJ-101", "--title", "Payment webhook", "--assignee", "dave", "--tool", "claude-code", "--repo", repo.Dir)
		d.Run("log", "PROJ-101", "Stripe test keys only work in test mode.", "--type", "gotcha")
		d.RunEscalate("PROJ-101", "Which currency rounding rule for JPY?")
		d.Run("attempt", "new", "PROJ-101", "--tool", "aider", "--repo", repo.Dir)
	},
}

// ReviewReadyToMerge is a data root with one ticket claimed for review (→ Review),
// the board state a merge scenario starts from.
var ReviewReadyToMerge = Fixture{
	Name: "review-ready-to-merge",
	Seed: func(d *Draiver, repo *GitRepo) {
		d.Run("new", "PROJ-102", "--title", "Search index", "--assignee", "dave", "--repo", repo.Dir)
		d.Run("log", "PROJ-102", "Chose server-side pagination over client-side.", "--type", "decision")
		d.Run("review", "PROJ-102", "PR #142 open; tests green.")
	},
}
