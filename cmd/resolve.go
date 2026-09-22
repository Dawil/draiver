package cmd

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/repo"
)

var resolveByFlag string

var resolveCmd = &cobra.Command{
	Use:   "resolve TICKET ESCALATION_SEQ ANSWER",
	Short: "Answer an escalation, linking the resolution back to it",
	Long: "resolve appends a resolution event referencing the escalation by its seq\n" +
		"within the attempt. Escalation and resolution together form one durable artefact.\n" +
		"Target a specific attempt with --attempt (defaults to the latest).\n\n" +
		"With --by PARENT, a coordinator resolves one of its children's escalations\n" +
		"(the downward-resolution flow over the `wants:` edge): PARENT must `wants:`\n" +
		"TICKET, and the resolution is stamped with PARENT's provenance rather than a\n" +
		"human actor. It can only ever unblock — never ratify a child Review→Done.",
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		seq, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("escalation seq must be an integer: %q", args[1])
		}
		answer := args[2]

		root, err := resolveRoot()
		if err != nil {
			return err
		}
		if !root.Exists(id) {
			return fmt.Errorf("ticket %q not found under %s", id, root.Dir)
		}
		att, err := resolveAttempt(root, id)
		if err != nil {
			return err
		}

		r := repo.New(root, resolveActor())
		if resolveByFlag != "" {
			e, err := r.ResolveBy(resolveByFlag, id, att, seq, answer)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "resolved %s/%s #%d -> resolution #%d (by %s)\n",
				id, att, seq, e.Seq, resolveByFlag)
			return nil
		}
		e, err := r.Resolve(id, att, seq, answer)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "resolved %s/%s #%d -> resolution #%d\n", id, att, seq, e.Seq)
		return nil
	},
}

func init() {
	resolveCmd.Flags().StringVar(&resolveByFlag, "by", "", "resolve on behalf of a parent Capability that `wants:` this ticket (downward resolution)")
	rootCmd.AddCommand(resolveCmd)
}
