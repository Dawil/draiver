package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"draiver/internal/event"
)

var escalateArtefacts []string

var escalateCmd = &cobra.Command{
	Use:   "escalate TICKET QUESTION",
	Short: "Raise an escalation and halt with a nonzero exit code",
	Long: "escalate appends an escalation event and exits " +
		fmt.Sprintf("%d", ExitEscalated) + " so the supervising process stops. The gate\n" +
		"is enforced by process control, not agent goodwill: do not guess past it.\n" +
		"Capture the surrounding context in a note/gotcha first so the escalation is\n" +
		"self-contained.",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, question := args[0], args[1]
		e, att, err := appendEvent(id, event.Event{
			Type:      "escalation",
			Artefacts: escalateArtefacts,
			Body:      question,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "escalated %s/%s #%d — halting (exit %d). Resolve with: draiver resolve %s %d \"...\" --attempt %s\n",
			id, att, e.Seq, ExitEscalated, id, e.Seq, att)
		return &exitError{code: ExitEscalated}
	},
}

func init() {
	escalateCmd.Flags().StringSliceVar(&escalateArtefacts, "artefact", nil, "path under artefacts/ (repeatable)")
	rootCmd.AddCommand(escalateCmd)
}
