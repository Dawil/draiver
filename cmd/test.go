package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/bddartefact"
	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/worktree"
)

// testLog switches on the durable-record mode: refuse a dirty tree, run, and on
// all-green append one hash-chained test-result event. Bare (off) is the agent's
// fast iterative loop — run + report only, no write, works against a dirty tree.
var testLog bool

var testCmd = &cobra.Command{
	Use:   "test [RUNG]",
	Short: "Run the worktree's test pyramid; --log records a passing result",
	Long: "test is the executor for a repo's .test-pyramid.yaml. Run from inside an\n" +
		"attempt's worktree — the checkout is the code under test, and test derives\n" +
		"which attempt from the branch it stands on. It climbs from the base rung up to\n" +
		"RUNG (default: the top rung), running each level's command and stopping at the\n" +
		"first non-green rung.\n" +
		"\n" +
		"Bare, it only runs and reports — the fast iterative loop, fine against a dirty\n" +
		"tree. With --log it refuses a dirty tree, then on all-green through RUNG appends\n" +
		"one durable test-result event carrying the rung and the HEAD commit — so brief\n" +
		"replays \"rung green @ sha\" for the next agent. A failing run logs nothing.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}

		p, err := pyramid.Load(wd)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if p == nil {
			// A repo that declares no pyramid is a valid state, not an error: there is
			// simply nothing to run.
			fmt.Fprintf(out, "no %s in %s — nothing to test\n", pyramid.FileName, wd)
			return nil
		}

		target := p.Target().Name
		if len(args) == 1 {
			target = args[0]
		}
		levels, ok := levelsUpTo(p, target)
		if !ok {
			return fmt.Errorf("rung %q is not a level in %s (levels: %s)", target, pyramid.FileName, rungNames(p))
		}

		// --log demands a commit that fully captures the tree under test, so refuse a
		// dirty tree up front — before running anything — rather than run and then find
		// there is no clean commit to attribute the result to.
		if testLog {
			dirty, err := worktree.DirtyAt(ctx, wd)
			if err != nil {
				return err
			}
			if dirty {
				return fmt.Errorf("working tree is dirty: commit before `test --log` so the recorded result names a commit that captures the tree tested")
			}
		}

		// Climb base→target, streaming each rung's output, stopping at the first that
		// is not green. A failing rung returns a plain error (exit 1) and — crucially —
		// logs nothing, so the log holds only passing results by construction.
		for _, lv := range levels {
			fmt.Fprintf(out, "== %s: %s\n", lv.Name, lv.Run)
			rc := exec.CommandContext(ctx, "sh", "-c", lv.Run)
			rc.Dir = wd
			rc.Stdout = cmd.OutOrStdout()
			rc.Stderr = cmd.ErrOrStderr()
			rc.Stdin = os.Stdin
			if err := rc.Run(); err != nil {
				return fmt.Errorf("rung %q not green: %w", lv.Name, err)
			}
		}
		fmt.Fprintf(out, "all green through %q\n", target)

		if !testLog {
			return nil
		}

		// All green and the tree was clean: attribute the result to the exact commit
		// that was tested and append the one durable event.
		sha, err := worktree.HeadSHA(ctx, wd)
		if err != nil {
			return err
		}
		key, err := worktree.Identify(ctx, wd)
		if err != nil {
			return err
		}
		root, err := resolveRoot()
		if err != nil {
			return err
		}

		// If the target rung declares output artefacts (a BDD/acceptance rung), capture
		// them into the attempt's artefacts/ store under a per-run key and reference the
		// set from the test-result event, so the evidence is provenance-anchored and
		// audit-covered (drv-017). An ordinary rung declares none and this is a no-op.
		arts, runKey, err := captureBDDArtefacts(out, root, key, p.Levels[len(levels)-1], sha)
		if err != nil {
			return err
		}

		ev, _, err := appendEventAt(root, key.Ticket, key.Attempt, event.Event{
			Type:      "test-result",
			Rung:      target,
			Commit:    sha,
			Artefacts: arts,
			Body:      testResultBody(target, sha, runKey),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "recorded test-result #%d on %s/%s (rung %q @ %s)\n", ev.Seq, key.Ticket, key.Attempt, target, shortSHA(sha))
		return nil
	},
}

// captureBDDArtefacts captures a BDD/acceptance rung's declared output files into
// the attempt's artefacts/ store under a per-run key, enforces the configured
// retention cap, and returns the artefacts-relative refs for the test-result event
// plus the run key (empty when the rung declares no artefacts — the ordinary case).
// It is best-effort on context that later tickets fill in: the environment is read
// as a bare label (drv-012 lifecycle pending) and the healthcheck verdict is left
// blank until environments land.
func captureBDDArtefacts(out io.Writer, root store.Root, key worktree.Key, target pyramid.Level, sha string) ([]string, string, error) {
	if len(target.Artifacts) == 0 {
		return nil, "", nil
	}
	sources := make([]bddartefact.Source, len(target.Artifacts))
	for i, a := range target.Artifacts {
		sources[i] = bddartefact.Source{Path: a}
	}
	rc := bddartefact.RunContext{
		Rung:        target.Name,
		Environment: target.Environment,
		Commit:      sha,
		Runstamp:    time.Now().UTC().Format("20060102T150405Z"),
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, "", fmt.Errorf("resolve working directory: %w", err)
	}
	artDir := root.ArtefactsDir(key.Ticket, key.Attempt)
	runKey, refs, err := bddartefact.Capture(artDir, wd, rc, sources)
	if err != nil {
		return nil, "", fmt.Errorf("capture BDD artefacts: %w", err)
	}
	fmt.Fprintf(out, "captured %d BDD artefact(s) under %s\n", len(refs), runKey)

	cfg, err := config.Load("")
	if err != nil {
		return nil, "", err
	}
	removed, err := bddartefact.Prune(artDir, rc.Rung, rc.Env(), cfg.BDDArtefactKeep)
	if err != nil {
		return nil, "", fmt.Errorf("prune BDD artefacts: %w", err)
	}
	if len(removed) > 0 {
		fmt.Fprintf(out, "pruned %d old BDD run(s) (keep=%d)\n", len(removed), cfg.BDDArtefactKeep)
	}
	return refs, runKey, nil
}

// testResultBody renders the test-result event body, noting the captured run key
// when a BDD rung produced artefacts.
func testResultBody(target, sha, runKey string) string {
	if runKey == "" {
		return fmt.Sprintf("Rung `%s` green @ `%s`.", target, shortSHA(sha))
	}
	return fmt.Sprintf("Rung `%s` green @ `%s`. BDD artefacts captured under `%s`.", target, shortSHA(sha), runKey)
}

// levelsUpTo returns the rungs from the base up to and including the one named
// target, in climb order. ok is false when no rung carries that name.
func levelsUpTo(p *pyramid.Pyramid, target string) ([]pyramid.Level, bool) {
	for i, lv := range p.Levels {
		if lv.Name == target {
			return p.Levels[:i+1], true
		}
	}
	return nil, false
}

// rungNames lists a pyramid's rung names in climb order, for an error that names
// what the caller could have asked for.
func rungNames(p *pyramid.Pyramid) string {
	names := make([]string, len(p.Levels))
	for i, lv := range p.Levels {
		names[i] = lv.Name
	}
	return strings.Join(names, ", ")
}

func init() {
	testCmd.Flags().BoolVar(&testLog, "log", false, "on all-green, append a durable test-result event (refuses a dirty tree)")
	rootCmd.AddCommand(testCmd)
}
