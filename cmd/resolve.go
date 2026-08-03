package cmd

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"draiver/internal/event"
	"draiver/internal/ticketlog"
)

var resolveCmd = &cobra.Command{
	Use:   "resolve TICKET ESCALATION_SEQ ANSWER",
	Short: "Answer an escalation, linking the resolution back to it",
	Long: "resolve appends a resolution event referencing the escalation by its seq\n" +
		"within the attempt. Escalation and resolution together form one durable artefact.\n" +
		"Target a specific attempt with --attempt (defaults to the latest).",
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
		events, err := ticketlog.Read(root, id, att)
		if err != nil {
			return err
		}
		var found *event.Event
		for i := range events {
			if events[i].Seq == seq {
				found = &events[i]
				break
			}
		}
		if found == nil {
			return fmt.Errorf("no event #%d on %s/%s", seq, id, att)
		}
		if found.Type != "escalation" {
			return fmt.Errorf("event #%d on %s/%s is a %q, not an escalation", seq, id, att, found.Type)
		}

		e, _, err := appendEvent(id, event.Event{
			Type: "resolution",
			Refs: []int{seq},
			Body: answer,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "resolved %s/%s #%d -> resolution #%d\n", id, att, seq, e.Seq)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(resolveCmd)
}
