package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
	"github.com/Dawil/draiver/internal/worktree"
)

// syncSpawn substitutes the detached re-exec with an in-process synchronous climb,
// so a test drives the whole launch→result arc deterministically without setsid or
// a second binary. It mirrors defaultSpawnDetachedTest's signature.
func syncSpawn(dataDir string, key worktree.Key, target, wd string, launchSeq int, logRel, actor string) error {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return runDetachedTest(context.Background(), cmd, key.Ticket, key.Attempt, target, wd, launchSeq, logRel)
}

// resultOutcomes returns the Outcome of every test-result on the attempt's log, in
// order — the probe for which verdict(s) a detached run landed.
func resultOutcomes(t *testing.T, data string) []string {
	t.Helper()
	events, err := ticketlog.Read(store.Root{Dir: data}, "PROJ-1", "0001")
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var out []string
	for _, e := range events {
		if e.Type == "test-result" {
			out = append(out, e.Outcome)
		}
	}
	return out
}

func asyncJobCount(t *testing.T, data string) int {
	t.Helper()
	events, err := ticketlog.Read(store.Root{Dir: data}, "PROJ-1", "0001")
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.Type == "async-job" {
			n++
		}
	}
	return n
}

// TestTestDetachLaunchesAndLandsResult is drvctl-055 AC #1: `draiver test --detach`
// on a clean tree exits 0 at once (so `&& draiver park` proceeds), records an
// `async-job` launch marker, and the out-of-band climb lands a durable pass
// `test-result` answering it. With the unit rung green, the verdict is pass.
func TestTestDetachLaunchesAndLandsResult(t *testing.T) {
	data, _, wt := pyramidAttempt(t, [][2]string{{"unit", "true"}})
	t.Chdir(wt)

	orig := spawnDetachedTest
	spawnDetachedTest = syncSpawn
	t.Cleanup(func() { spawnDetachedTest = orig })

	out, code := run(t, "--data", data, "--actor", "agent:test", "test", "--detach")
	if code != 0 {
		t.Fatalf("test --detach on a clean tree should exit 0 (launch ok), got %d: %s", code, out)
	}
	if n := asyncJobCount(t, data); n != 1 {
		t.Fatalf("a detached launch must record exactly one async-job marker, got %d", n)
	}
	if got := resultOutcomes(t, data); len(got) != 1 || got[0] != "pass" {
		t.Fatalf("a green detached climb must land exactly one pass test-result, got %v", got)
	}
}

// TestTestDetachRedClimbLandsFail pins the pass/fail distinction: a detached climb
// whose rung is red lands a `fail` test-result (so the worker is woken to iterate)
// — never a pass that the review gate would read as green.
func TestTestDetachRedClimbLandsFail(t *testing.T) {
	data, _, wt := pyramidAttempt(t, [][2]string{{"unit", "false"}}) // `false` exits 1 → red
	t.Chdir(wt)

	orig := spawnDetachedTest
	spawnDetachedTest = syncSpawn
	t.Cleanup(func() { spawnDetachedTest = orig })

	// The launch itself still succeeds (exit 0) — the red verdict lands on the log.
	if out, code := run(t, "--data", data, "--actor", "agent:test", "test", "--detach"); code != 0 {
		t.Fatalf("a launch whose climb later goes red still exits 0, got %d: %s", code, out)
	}
	if got := resultOutcomes(t, data); len(got) != 1 || got[0] != "fail" {
		t.Fatalf("a red detached climb must land exactly one fail test-result, got %v", got)
	}
}

// TestTestDetachLaunchFailureAnswersMarker is drvctl-055 AC #4's spawn-side: if the
// detached runner cannot even start, the command exits nonzero (so `&& draiver park`
// short-circuits and the worker stays live) AND lands a failing result answering the
// launch marker, so no dangling wake source could park the worker to sleep forever.
func TestTestDetachLaunchFailureAnswersMarker(t *testing.T) {
	data, _, wt := pyramidAttempt(t, [][2]string{{"unit", "true"}})
	t.Chdir(wt)

	orig := spawnDetachedTest
	spawnDetachedTest = func(string, worktree.Key, string, string, int, string, string) error {
		return context.DeadlineExceeded // stand in for a spawn failure
	}
	t.Cleanup(func() { spawnDetachedTest = orig })

	out, code := run(t, "--data", data, "--actor", "agent:test", "test", "--detach")
	if code == 0 {
		t.Fatalf("a spawn failure must exit nonzero so `&& park` short-circuits, got 0: %s", out)
	}
	// The launch marker was written, then answered by a fail result — no source dangles.
	if n := asyncJobCount(t, data); n != 1 {
		t.Fatalf("the launch marker should still be recorded, got %d async-job events", n)
	}
	if got := resultOutcomes(t, data); len(got) != 1 || got[0] != "fail" {
		t.Fatalf("a spawn failure must answer its own marker with a fail result, got %v", got)
	}
}

// TestTestDetachRefusesDirtyTree: like `--log`, a detached launch demands a clean
// tree so the result it records names a commit that captures the tree tested. A
// dirty tree is refused with no launch marker written.
func TestTestDetachRefusesDirtyTree(t *testing.T) {
	data, _, wt := pyramidAttempt(t, [][2]string{{"unit", "true"}})
	t.Chdir(wt)
	// Dirty the checkout after the clean pyramid commit.
	if err := os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code, err := runE(t, "--data", data, "--actor", "agent:test", "test", "--detach")
	if code == 0 {
		t.Fatalf("a dirty tree must be refused, got exit 0: %s", out)
	}
	if msg := errText(err); msg == "" {
		t.Fatal("refusal should carry a message")
	}
	if n := asyncJobCount(t, data); n != 0 {
		t.Fatalf("a refused dirty-tree launch must write no async-job marker, got %d", n)
	}
}
