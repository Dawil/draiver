package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/project"
)

var (
	attemptTool  string
	attemptModel string
	attemptRepo  string
	attemptBase  string
	attemptFrom  string
)

var attemptCmd = &cobra.Command{
	Use:   "attempt",
	Short: "Manage a ticket's attempts (independent journeys with their own log)",
}

var attemptNewCmd = &cobra.Command{
	Use:   "new TICKET",
	Short: "Start a new attempt on a ticket (its own log, hash chain, working tree)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		if !root.Exists(id) {
			return fmt.Errorf("ticket %q not found under %s", id, root.Dir)
		}
		if attemptFrom != "" && !root.AttemptExists(id, attemptFrom) {
			return fmt.Errorf("--from attempt %s/%s not found", id, attemptFrom)
		}
		// Repo and base are ticket-level attributes recorded per attempt, so a --from
		// attempt inherits its parent's unless overridden — a sibling attempt targets
		// the same working tree and lands into the same base by default.
		repo, base := attemptRepo, attemptBase
		if attemptFrom != "" && (repo == "" || base == "") {
			parent, err := attempt.LoadMeta(root, id, attemptFrom)
			if err != nil {
				return err
			}
			if repo == "" {
				repo = parent.Repo
			}
			if base == "" {
				base = parent.Base
			}
		}
		// --repo is required unless --from supplies it by inheritance; refuse a
		// repo-less attempt up front rather than defer the failure to the daemon
		// (drvctl-017). attempt.Create is the belt; this is the friendlier message.
		if strings.TrimSpace(repo) == "" {
			return fmt.Errorf("a repo path is required: pass --repo <local git working tree>, or --from an attempt that records one")
		}
		// Base defaults to the repo's current branch when neither --base nor an
		// inheriting --from supplied one (drvctl-021).
		if strings.TrimSpace(base) == "" {
			if base, err = resolveBase(cmd.Context(), repo, ""); err != nil {
				return err
			}
		}
		m, err := attempt.Create(root, id, attempt.New{
			Tool:  attemptTool,
			Model: attemptModel,
			Repo:  repo,
			Base:  base,
			Actor: resolveActor(),
			From:  attemptFrom,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "started %s attempt %s\n", id, m.ID)
		return nil
	},
}

var attemptLsCmd = &cobra.Command{
	Use:   "ls TICKET",
	Short: "List a ticket's attempts with tool, model, and derived state",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		if !root.Exists(id) {
			return fmt.Errorf("ticket %q not found under %s", id, root.Dir)
		}
		ids, err := root.ListAttempts(id)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "%s has no attempts\n", id)
			return nil
		}
		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ATTEMPT\tTOOL\tMODEL\tSTATE\tEVENTS")
		for _, aid := range ids {
			a, err := project.LoadAttempt(root, id, aid)
			if err != nil {
				return err
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", a.ID, dash(a.Tool), dash(a.Model), a.State, len(a.Events))
		}
		return tw.Flush()
	},
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func init() {
	attemptNewCmd.Flags().StringVar(&attemptTool, "tool", "", "coding-agent tool (e.g. claude-code, aider, codex)")
	attemptNewCmd.Flags().StringVar(&attemptModel, "model", "", "model (e.g. opus-4.8)")
	attemptNewCmd.Flags().StringVar(&attemptRepo, "repo", "", "required (unless inherited via --from): local path to the git working tree this attempt targets")
	attemptNewCmd.Flags().StringVar(&attemptBase, "base", "", "branch this attempt lands back into (default: inherited via --from, else the repo's current branch)")
	attemptNewCmd.Flags().StringVar(&attemptFrom, "from", "", "record provenance: this attempt branches from attempt <id>")
	attemptCmd.AddCommand(attemptNewCmd)
	attemptCmd.AddCommand(attemptLsCmd)
	rootCmd.AddCommand(attemptCmd)
}
