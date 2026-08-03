package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"draiver/internal/event"
)

var reviewCmd = &cobra.Command{
	Use:   "review TICKET [CLAIM]",
	Short: "Claim a ticket is done (a claim, not a fact, for a human to verify)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		body := "Agent claims the ticket is complete; ready for review."
		if len(args) == 2 {
			body = args[1]
		}
		e, err := appendEvent(id, event.Event{Type: "review", Body: body})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "review claimed on %s (seq %d)\n", id, e.Seq)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(reviewCmd)
}
