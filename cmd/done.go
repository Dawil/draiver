package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"draiver/internal/event"
)

var doneCmd = &cobra.Command{
	Use:   "done TICKET [NOTE]",
	Short: "Close a ticket (terminal)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		body := "Ticket closed."
		if len(args) == 2 {
			body = args[1]
		}
		e, err := appendEvent(id, event.Event{Type: "done", Body: body})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "closed %s (seq %d)\n", id, e.Seq)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(doneCmd)
}
