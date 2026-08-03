package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"draiver/internal/audit"
)

var auditCmd = &cobra.Command{
	Use:   "audit TICKET",
	Short: "Verify the hash-chained log; nonzero exit if tampered",
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
		res, err := audit.VerifyTicket(root, id)
		if err != nil {
			return err
		}
		if !res.OK {
			fmt.Fprintf(cmd.OutOrStdout(), "FAIL %s: %s\n", id, res.Reason)
			return &exitError{code: ExitAuditFailed, msg: ""}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "OK %s: %d events, chain intact\n", id, res.Count)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(auditCmd)
}
