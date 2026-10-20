package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/wake"
	"github.com/Dawil/draiver/internal/worktree"
)

// test_detach.go implements drvctl-055's detached/tracked `draiver test`: the first
// consumer of `park` as a general primitive. A worker that must run a long test no
// longer holds a live agent (and its context, and token burn) idling in-session —
// it fires the climb off out-of-band and parks, woken when the result lands on its
// own log.
//
// The shape is `draiver test --detach && draiver park`:
//
//   - `--detach` records an `async-job` launch marker on the attempt's own log (the
//     wake source `draiver park` and the completion gate read via internal/wake),
//     spawns a detached runner that outlives the session, and returns 0.
//   - `draiver park` then suspends the worker to Pending (not Needs-me): the launch
//     marker is a pending wake source, so the park is legitimate and no escalation
//     is raised.
//   - the detached runner climbs the pyramid and lands a durable `test-result`
//     (Outcome pass|fail) that answers the launch. That result is a doorbell on the
//     worker's own log; the reconciler path-activates the parked worker (a `woken-by`
//     event, drvctl-051) to claim review (green) or iterate (red).
//
// If the launch fails the command exits nonzero, so the `&&` short-circuits and the
// worker stays live — park's no-wake-source refusal (drvctl-052) backs this up.

// asyncJobType is the launch marker a detached test writes to register the wake
// source. Kept as a local literal, matching the rest of the log types (there is no
// central registry); it is mirrored in internal/wake and internal/reconcile.
const asyncJobType = "async-job"

var (
	// testDetach is the `--detach` flag on `draiver test`.
	testDetach bool

	// Flags on the hidden `__detached-test` runner — set only on the re-exec'd child
	// the spawner launches, never by a human.
	detachTicket    string
	detachAttempt   string
	detachWorkdir   string
	detachRung      string
	detachLaunchSeq int
	detachLogRel    string
)

// spawnDetachedTest fires the out-of-band runner that climbs the pyramid and lands
// the result, detached from the agent's session (a new session via Setsid) so it
// survives the `draiver park` that reaps the session. It is a package var so tests
// can substitute a synchronous in-process runner for a real re-exec.
var spawnDetachedTest = defaultSpawnDetachedTest

// launchDetached records the async-job launch marker, fires the detached runner,
// and returns at once (exit 0) so the agent's `&& draiver park` proceeds. It
// demands a clean tree up front — exactly as `--log` does — so the result the
// runner lands names a commit that captures the tree under test. Spawn-then-answer
// is the failure discipline: the launch marker is written first so park has a wake
// source, and a spawn failure lands a failing result that answers it, leaving no
// dangling source that could park the worker to sleep forever.
func launchDetached(ctx context.Context, cmd *cobra.Command, wd, target string, levels []pyramid.Level) error {
	out := cmd.OutOrStdout()

	dirty, err := worktree.DirtyAt(ctx, wd)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("working tree is dirty: commit before `test --detach` so the recorded result names a commit that captures the tree tested")
	}

	key, err := worktree.Identify(ctx, wd)
	if err != nil {
		return err
	}
	root, err := resolveRoot()
	if err != nil {
		return err
	}

	// The launch marker: a durable wake source on the attempt's own log, carrying the
	// target rung so the resuming agent (and the board) can see what is running.
	launch, _, err := appendEventAt(root, key.Ticket, key.Attempt, event.Event{
		Type: asyncJobType,
		Rung: target,
		Body: launchBody(target),
	})
	if err != nil {
		return err
	}

	logRel := detachedLogRel(launch.Seq)
	if err := spawnDetachedTest(root.Dir, key, target, wd, launch.Seq, logRel, resolveActor()); err != nil {
		// The runner never started, so nothing will ever answer the launch marker.
		// Land a failing result that answers it ourselves, so the worker is never
		// parked against a source that cannot fire, and exit nonzero so `&& park`
		// short-circuits.
		if _, _, aerr := answerLaunchFailed(root, key.Ticket, key.Attempt, launch.Seq, target,
			fmt.Sprintf("Detached test runner failed to start: %v. No climb ran.", err)); aerr != nil {
			return fmt.Errorf("spawn detached test (%v); and answering the launch marker: %w", err, aerr)
		}
		return fmt.Errorf("spawn detached test: %w", err)
	}

	fmt.Fprintf(out, "launched detached test (rung %q) on %s/%s (async-job #%d); results will land on the log — `draiver park %s` now, you will be woken when it finishes\n",
		target, key.Ticket, key.Attempt, launch.Seq, key.Ticket)
	return nil
}

// answerLaunchFailed is the cmd-side adapter over wake.AsyncLaunchFailResult (the one
// terminal-result discipline, drv-031): it stamps the operator/agent actor and runs
// repo validation via appendEventAt. Shared by launchDetached's spawn-failure path
// above and `draiver park --clear` of an async-job source, so neither leaves a
// dangling wake source the worker could park against forever.
func answerLaunchFailed(root store.Root, ticket, att string, launchSeq int, rung, body string) (event.Event, string, error) {
	return appendEventAt(root, ticket, att, wake.AsyncLaunchFailResult(launchSeq, rung, body))
}

// detachedLogRel is the artefacts-relative path the detached runner streams its
// console output to — a per-launch pointer the result event carries so a reviewer
// (and a red-iterating agent) can read what the climb did.
func detachedLogRel(launchSeq int) string {
	return filepath.Join("detached", fmt.Sprintf("test-%04d.log", launchSeq))
}

// launchBody renders the async-job launch marker (the board renders bodies as
// Markdown).
func launchBody(target string) string {
	return fmt.Sprintf("Launched a **detached** `draiver test` climbing to rung `%s`. This attempt is now parkable: the result will land as a `test-result` on this log and wake it. Run `draiver park` next.", target)
}

// defaultSpawnDetachedTest re-execs this binary as the hidden `__detached-test`
// runner in a fresh session (Setsid), so reaping the agent's session/process group
// does not kill the climb. Its stdout/stderr stream to the per-launch artefact log.
func defaultSpawnDetachedTest(dataDir string, key worktree.Key, target, wd string, launchSeq int, logRel, actor string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own executable: %w", err)
	}

	logAbs := filepath.Join(store.Root{Dir: dataDir}.ArtefactsDir(key.Ticket, key.Attempt), logRel)
	if err := os.MkdirAll(filepath.Dir(logAbs), 0o755); err != nil {
		return fmt.Errorf("prepare detached log dir: %w", err)
	}
	lf, err := os.Create(logAbs)
	if err != nil {
		return fmt.Errorf("open detached log: %w", err)
	}
	defer lf.Close()

	args := []string{"__detached-test",
		"--data", dataDir,
		"--actor", actor,
		"--ticket", key.Ticket,
		"--attempt", key.Attempt,
		"--rung", target,
		"--workdir", wd,
		"--launch-seq", strconv.Itoa(launchSeq),
		"--logrel", logRel,
	}
	c := exec.Command(exe, args...)
	c.Dir = wd
	c.Stdout = lf
	c.Stderr = lf
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return c.Start() // detached: do not Wait — the result lands on the log.
}

// detachedTestCmd is the hidden runner the spawner re-execs. It runs the climb
// synchronously and lands exactly one durable `test-result` (Outcome pass|fail)
// answering the launch marker — never meant to be invoked by a human.
var detachedTestCmd = &cobra.Command{
	Use:    "__detached-test",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDetachedTest(cmd.Context(), cmd, detachTicket, detachAttempt, detachRung, detachWorkdir, detachLaunchSeq, detachLogRel)
	},
}

// runDetachedTest is the detached runner's body, factored out so a test can drive it
// in-process: it climbs base→rung in workdir and appends one `test-result`
// answering the launch marker. A green target records Outcome=pass with the HEAD
// commit (so the review gate counts it) plus any BDD artefacts; a red target records
// Outcome=fail so the worker is woken to iterate but the gate never reads it as
// green (cmd/review.go). The console-log pointer rides on both.
func runDetachedTest(ctx context.Context, cmd *cobra.Command, ticket, att, rung, wd string, launchSeq int, logRel string) error {
	out := cmd.OutOrStdout()
	root, err := resolveRoot()
	if err != nil {
		return err
	}

	p, err := pyramid.Load(wd)
	if err != nil {
		return err
	}
	if p == nil {
		// The pyramid vanished between launch and run — answer the marker as a failure
		// rather than leave the worker parked forever.
		return recordDetachedResult(root, ticket, att, rung, "", launchSeq, logRel, false, nil,
			fmt.Sprintf("No %s in %s at run time — nothing to test.", pyramid.FileName, wd))
	}
	levels, ok := levelsUpTo(p, rung)
	if !ok {
		return recordDetachedResult(root, ticket, att, rung, "", launchSeq, logRel, false, nil,
			fmt.Sprintf("Rung %q is no longer a level in %s (levels: %s).", rung, pyramid.FileName, rungNames(p)))
	}

	targetGreen, advisoryReds, failErr := climb(ctx, cmd, wd, p, levels)

	sha, shaErr := worktree.HeadSHA(ctx, wd)
	if shaErr != nil {
		// Without a commit we cannot attribute a green result, so treat it as a failure
		// to record rather than fabricate provenance.
		return recordDetachedResult(root, ticket, att, rung, "", launchSeq, logRel, false, nil,
			fmt.Sprintf("Could not resolve HEAD to attribute the result: %v.", shaErr))
	}

	if !targetGreen {
		why := "a required rung was not green"
		if failErr != nil {
			why = failErr.Error()
		}
		if len(advisoryReds) > 0 {
			why = fmt.Sprintf("%s (advisory rung(s) also red: %v)", why, advisoryReds)
		}
		fmt.Fprintf(out, "detached climb red through %q: %s\n", rung, why)
		return recordDetachedResult(root, ticket, att, rung, sha, launchSeq, logRel, false, nil,
			fmt.Sprintf("Rung `%s` **not green** @ `%s`: %s. Iterate, then relaunch `draiver test --detach && draiver park`.", rung, shortSHA(sha), why))
	}

	// Green: capture the target rung's BDD evidence (if any) alongside the console log.
	arts, runKey, err := captureBDDArtefacts(out, root, worktree.Key{Ticket: ticket, Attempt: att}, p.Levels[len(levels)-1], sha)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "detached climb green through %q @ %s\n", rung, shortSHA(sha))
	return recordDetachedResult(root, ticket, att, rung, sha, launchSeq, logRel, true, arts,
		detachedResultBody(rung, sha, runKey))
}

// recordDetachedResult appends the one `test-result` the detached runner lands. The
// console-log pointer leads the artefact set so a fail (which captures no BDD
// evidence) still carries something to read.
func recordDetachedResult(root store.Root, ticket, att, rung, sha string, launchSeq int, logRel string, pass bool, bddArts []string, body string) error {
	outcome := "fail"
	if pass {
		outcome = "pass"
	}
	arts := append([]string{logRel}, bddArts...)
	_, _, err := appendEventAt(root, ticket, att, event.Event{
		Type:      "test-result",
		Outcome:   outcome,
		Rung:      rung,
		Commit:    sha,
		Refs:      []int{launchSeq},
		Artefacts: arts,
		Body:      body,
	})
	return err
}

// detachedResultBody renders a green detached result, noting any captured BDD run.
func detachedResultBody(rung, sha, runKey string) string {
	if runKey == "" {
		return fmt.Sprintf("Rung `%s` **green** @ `%s` (detached). Claim review.", rung, shortSHA(sha))
	}
	return fmt.Sprintf("Rung `%s` **green** @ `%s` (detached). BDD artefacts under `%s`. Claim review.", rung, shortSHA(sha), runKey)
}

func init() {
	f := detachedTestCmd.Flags()
	f.StringVar(&detachTicket, "ticket", "", "ticket the detached runner records against")
	f.StringVar(&detachAttempt, "attempt", "", "attempt the detached runner records against")
	f.StringVar(&detachRung, "rung", "", "target rung to climb to")
	f.StringVar(&detachWorkdir, "workdir", "", "worktree the climb runs in")
	f.IntVar(&detachLaunchSeq, "launch-seq", 0, "seq of the async-job launch marker this result answers")
	f.StringVar(&detachLogRel, "logrel", "", "artefacts-relative path of the runner's console log")
	rootCmd.AddCommand(detachedTestCmd)
}
