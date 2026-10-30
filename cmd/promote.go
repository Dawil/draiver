package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/promote"
	"github.com/Dawil/draiver/internal/pyramid"
	"github.com/Dawil/draiver/internal/worktree"
)

// promoteLog switches on the durable-record mode: refuse a dirty tree, verify, and on
// a green qa append one hash-chained promotion-result event. Bare (off) is the fast
// iterative check — verify + report only, no write, works against a dirty tree.
var promoteLog bool

var promoteCmd = &cobra.Command{
	Use:   "promote <environment>",
	Short: "Verify (don't perform) a promotion to an environment; --log records a passing result",
	Long: "promote VERIFIES a repo-declared environment promotion — it never performs one.\n" +
		"draiver does not deploy, merge, or push anything to an environment; it only runs\n" +
		"the `promotions:` entry's repo-authored `qa` success check over that environment\n" +
		"(drv-043), exactly as `draiver test` runs a rung. Run from inside an attempt's\n" +
		"worktree — the checkout is the code whose promotion is verified, and promote\n" +
		"derives which attempt from the branch it stands on.\n" +
		"\n" +
		"It brings the environment `up` (if declared), runs its healthchecks, runs `qa`,\n" +
		"and always runs `down` — exiting 0 on qa success, 1 on qa failure, with the same\n" +
		"env-up/blocked exit codes for infra/dependency faults `draiver test` uses.\n" +
		"Promotions are a separate, informational axis: they never gate review or done,\n" +
		"and `draiver test` never runs them.\n" +
		"\n" +
		"Bare, it only verifies and reports — the fast loop, fine against a dirty tree.\n" +
		"With --log it refuses a dirty tree, then on a green qa appends one durable\n" +
		"promotion-result event carrying the environment and the HEAD commit it verified,\n" +
		"so the board shows the attempt promoted-at-HEAD. A failing qa logs nothing.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		envName := strings.TrimSpace(args[0])
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}

		out := cmd.OutOrStdout()
		p, active, extraEnv, err := resolvePyramid(ctx, wd)
		if err != nil {
			return err
		}
		if p == nil {
			// Neither an in-repo pyramid nor a resolvable overlay: nothing to promote.
			fmt.Fprintf(out, "no %s in %s and no overlay configured — nothing to promote\n", pyramid.FileName, wd)
			return nil
		}
		if active != nil {
			fmt.Fprintf(out, "using test-pyramid overlay %s (via %s) @ %s\n", active.Repo, active.Via, shortSHA(active.Commit))
			recordOverlayProvenance(cmd, wd, active)
		}

		promo, ok := p.Promotion(envName)
		if !ok {
			return fmt.Errorf("no promotion declares environment %q in %s (promotions: %s)", envName, pyramid.FileName, promotionNames(p))
		}
		env, _ := p.EnvironmentByName(promo.Environment)

		// --log demands a commit that fully captures the verified tree, so refuse a dirty
		// tree up front — before running anything — exactly as `test --log` does.
		if promoteLog {
			dirty, err := worktree.DirtyAt(ctx, wd)
			if err != nil {
				return err
			}
			if dirty {
				return fmt.Errorf("working tree is dirty: commit before `promote --log` so the recorded result names a commit that captures the tree verified")
			}
		}

		outcome := promote.RunQA(ctx, out, cmd.ErrOrStderr(), wd, env, promo, extraEnv)
		for _, l := range outcome.Lines {
			fmt.Fprintln(out, l)
		}

		switch outcome.Status {
		case promote.StatusEnvFault:
			return &exitError{code: ExitEnvUp, msg: fmt.Sprintf("environment %q up failed — infrastructure fault, not your code; escalate", promo.Environment)}
		case promote.StatusBlocked:
			return &exitError{code: ExitBlocked, msg: fmt.Sprintf("environment %q healthcheck red — dependency not ready (blocked, not a code fault)", promo.Environment)}
		case promote.StatusQAFailed:
			return &exitError{code: 1, msg: fmt.Sprintf("promotion qa for environment %q failed — not promoted", promo.Environment)}
		}

		// Green. Without --log this is the fast check — report and stop, write nothing.
		if !promoteLog {
			return nil
		}

		// Green and the tree was clean: attribute the result to the exact commit verified
		// and append the one durable event.
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
			Type:        "promotion-result",
			Environment: promo.Environment,
			Commit:      sha,
			Outcome:     "pass",
			Body:        promotionResultBody(promo.Environment, promo.Base, sha, true),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "recorded promotion-result #%d on %s/%s (environment %q @ %s)\n", ev.Seq, key.Ticket, key.Attempt, promo.Environment, shortSHA(sha))
		return nil
	},
}

// promotionResultBody renders a promotion-result event body (the board renders bodies
// as Markdown). pass distinguishes a verified promotion from a recorded failure; base,
// when declared, is noted as the branch the environment tracks.
func promotionResultBody(env, base, sha string, pass bool) string {
	verb := "**verified**"
	if !pass {
		verb = "**failed**"
	}
	if strings.TrimSpace(base) != "" {
		return fmt.Sprintf("Promotion to `%s` %s @ `%s` (tracks `%s`).", env, verb, shortSHA(sha), base)
	}
	return fmt.Sprintf("Promotion to `%s` %s @ `%s`.", env, verb, shortSHA(sha))
}

// promotionNames lists a pyramid's declared promotion environments, for an error that
// names what the caller could have asked for. "none" when the pyramid declares none.
func promotionNames(p *pyramid.Pyramid) string {
	if len(p.Promotions) == 0 {
		return "none"
	}
	names := make([]string, len(p.Promotions))
	for i, pr := range p.Promotions {
		names[i] = pr.Environment
	}
	return strings.Join(names, ", ")
}

func init() {
	promoteCmd.Flags().BoolVar(&promoteLog, "log", false, "on a green qa, append a durable promotion-result event (refuses a dirty tree)")
	rootCmd.AddCommand(promoteCmd)
}
