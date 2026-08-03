package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"draiver/internal/brief"
)

var briefCmd = &cobra.Command{
	Use:   "brief TICKET",
	Short: "Replay spec + log into a context blob that cold-starts a fresh agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		out, err := brief.Build(root, args[0])
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
