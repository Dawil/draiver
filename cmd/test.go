package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/repo"
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

		// An explicit RUNG wins. Otherwise the default climb target is the repo's
		// configured default_test_rung (drv-011) when it names a real rung — the
		// dev-iteration default (decision #4), which may sit *below* the file's top
		// rung — falling back to the file's top rung when unset or unknown. Resolved
		// best-effort from the worktree (branch → attempt → recorded repo → config);
		// any failure leaves the file's top rung, so `test` never breaks on a
		// config/identify error. The Review gate applies this same rung as a FLOOR
		// instead (it can only raise its bar, never lower it), so what `test` iterates
		// at by default may be lower than what `review` ultimately demands.
		target := p.Target().Name
		if rr := repoRungForWorktree(ctx, wd); rr != "" {
			if _, ok := levelsUpTo(p, rr); ok {
				target = rr
			}
		}
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
		ev, _, err := appendEventAt(root, key.Ticket, key.Attempt, event.Event{
			Type:   "test-result",
			Rung:   target,
			Commit: sha,
			Body:   fmt.Sprintf("Rung `%s` green @ `%s`.", target, shortSHA(sha)),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "recorded test-result #%d on %s/%s (rung %q @ %s)\n", ev.Seq, key.Ticket, key.Attempt, target, shortSHA(sha))
		return nil
	},
}

// repoRungForWorktree resolves the repo's configured default_test_rung (drv-011)
// for the attempt the worktree at wd belongs to, best-effort: it maps the checkout's
// branch to an attempt (worktree.Identify), reads that attempt's recorded repo, and
// resolves the per-repo setting through the gateway. It returns "" — leaving the
// caller on the file's top rung — on any failure (a non-attempt branch, a missing
// attempt, an unreadable config), so the fast `test` loop never breaks on config.
func repoRungForWorktree(ctx context.Context, wd string) string {
	key, err := worktree.Identify(ctx, wd)
	if err != nil {
		return ""
	}
	root, err := resolveRoot()
	if err != nil {
		return ""
	}
	meta, err := attempt.LoadMeta(root, key.Ticket, key.Attempt)
	if err != nil {
		return ""
	}
	rs, err := repo.New(root, resolveActor()).RepoSettings(meta.Repo)
	if err != nil {
		return ""
	}
	return rs.TestRung
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
