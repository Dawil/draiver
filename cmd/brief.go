package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/brief"
)

var briefCmd = &cobra.Command{
	Use:   "brief TICKET",
	Short: "Replay spec + an attempt's log into a context blob for a fresh agent",
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
		att, err := resolveAttempt(root, id)
		if err != nil {
			return err
		}
		out, err := brief.Build(root, id, att)
		if err != nil {
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), out)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(briefCmd)
}
