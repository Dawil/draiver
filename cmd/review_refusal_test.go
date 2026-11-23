package cmd

import (
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// twoRungPyramid fails `unit` the way `go test` does, so a detached climb records
// the failing test by name.
const twoRungPyramid = `levels:
  - name: unit
    run: "echo '--- FAIL: TestBroken (0.01s)'; echo 'FAIL\tgithub.com/x/y\t0.1s'; false"
  - name: acceptance
    run: "true"
`

// detachedRed drives a real detached climb in-process, landing its fail result.
func detachedRed(t *testing.T, data, wt string) {
	t.Helper()
	t.Chdir(wt)
	orig := spawnDetachedTest
	spawnDetachedTest = syncSpawn
	t.Cleanup(func() { spawnDetachedTest = orig })
	if out, code := run(t, "--data", data, "--actor", "agent:test", "test", "--detach"); code != 0 {
		t.Fatalf("test --detach exited %d: %s", code, out)
	}
}

// boardResultAt records a test-result on a second ticket's attempt (standing in for
// whatever attempt tested the base branch) at commit.
func boardResultAt(t *testing.T, data, repo string, e event.Event) {
	t.Helper()
	if _, code := run(t, "--data", data, "--actor", "human:test", "new", "PROJ-0", "--title", "main", "--repo", repo); code != 0 {
		t.Fatalf("new PROJ-0 exited %d", code)
	}
	e.Type, e.Actor = "test-result", "agent:test"
	if _, err := ticketlog.Append(store.Root{Dir: data}, "PROJ-0", "0001", e); err != nil {
		t.Fatal(err)
	}
}

func refusal(t *testing.T, data string) string {
	t.Helper()
	out, code, err := runE(t, "--data", data, "--actor", "agent:test", "review", "PROJ-1", "ready")
	if code == 0 {
		t.Fatalf("expected a refusal, got exit 0: %s", out)
	}
	return errText(err)
}

// A detached red climb records the rung it halted at and the failing test by name.
func TestDetachedFailRecordsFailedRungAndTests(t *testing.T) {
	data, _, wt := pyramidAttemptYAML(t, twoRungPyramid)
	detachedRed(t, data, wt)
	events, err := ticketlog.Read(store.Root{Dir: data}, "PROJ-1", "0001")
	if err != nil {
		t.Fatal(err)
	}
	var got *event.Event
	for i := range events {
		if events[i].Type == "test-result" {
			got = &events[i]
		}
	}
	if got == nil || got.Outcome != "fail" || got.FailedRung != "unit" || strings.Join(got.FailedTests, ",") != "TestBroken" {
		t.Fatalf("fail result = %+v, want failed_rung unit and failed_tests [TestBroken]", got)
	}
}

// drv-063 AC: the refusal names the red rung and its failing test, says the higher
// rung was never reached, and — when the base branch's attempt recorded the same rung
// red at the merge-base — that the failure is inherited from main.
func TestReviewRefusalNamesFailingTestAndInheritedRed(t *testing.T) {
	data, repo, wt := pyramidAttemptYAML(t, twoRungPyramid)
	detachedRed(t, data, wt)
	boardResultAt(t, data, repo, event.Event{
		Outcome: "fail", Rung: "acceptance", Commit: headOf(t, wt, "main"),
		FailedRung: "unit", FailedTests: []string{"TestBroken"}, Body: "main red",
	})
	msg := refusal(t, data)
	for _, want := range []string{
		`rung "unit": failing TestBroken`,
		`rung "acceptance": not reached — the climb halted at red rung "unit"`,
		"already red (failing TestBroken)",
		"Inherited from main, not introduced by this branch",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal missing %q:\n%s", want, msg)
		}
	}
}

// A rung green at the merge-base means this branch introduced the failure.
func TestReviewRefusalSaysIntroducedWhenGreenAtMergeBase(t *testing.T) {
	data, repo, wt := pyramidAttemptYAML(t, twoRungPyramid)
	detachedRed(t, data, wt)
	boardResultAt(t, data, repo, event.Event{Rung: "acceptance", Commit: headOf(t, wt, "main"), Body: "main green"})
	msg := refusal(t, data)
	if !strings.Contains(msg, ": green — test-result #") || !strings.Contains(msg, "This branch introduced the failure") {
		t.Fatalf("refusal should say the branch introduced the failure:\n%s", msg)
	}
}

// With nothing recorded at the merge-base, the refusal says so and how to check.
func TestReviewRefusalUnknownAtMergeBaseSaysHowToCheck(t *testing.T) {
	data, _, wt := pyramidAttemptYAML(t, twoRungPyramid)
	detachedRed(t, data, wt)
	msg := refusal(t, data)
	if !strings.Contains(msg, "no result recorded, so whether it is inherited is unknown") || !strings.Contains(msg, "git worktree add --detach /tmp/mb") {
		t.Fatalf("refusal should say the merge-base is unknown and how to check:\n%s", msg)
	}
}

// With no failing result at all (only synchronous runs), the refusal says how to get
// the failing test named.
func TestReviewRefusalWithoutFailRecordPointsAtDetach(t *testing.T) {
	data, _, _ := pyramidAttemptYAML(t, twoRungPyramid)
	msg := refusal(t, data)
	if !strings.Contains(msg, "`draiver test --detach` records which test fails") {
		t.Fatalf("refusal should point at --detach:\n%s", msg)
	}
}

func TestFailScannerNamesGoFailures(t *testing.T) {
	var f failScanner
	_, _ = f.Write([]byte("=== RUN TestA\n--- FAIL: TestA (0.00s)\n    --- FAIL: TestA/sub (0.0"))
	_, _ = f.Write([]byte("0s)\nFAIL\tgithub.com/x/y\t0.2s\n--- FAIL: TestA (0.00s)\n"))
	if got := strings.Join(f.failures(), ","); got != "TestA,TestA/sub" {
		t.Fatalf("failures = %q", got)
	}
	var b failScanner
	_, _ = b.Write([]byte("FAIL\tgithub.com/x/y [build failed]\n"))
	if got := strings.Join(b.failures(), ","); got != "github.com/x/y [build failed]" {
		t.Fatalf("build failure = %q", got)
	}
}
