package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"draiver/internal/audit"
)

var auditCmd = &cobra.Command{
	Use:   "audit TICKET",
	Short: "Verify the hash-chained log of each attempt; nonzero exit if tampered",
	Long: "audit verifies every attempt's chain on the ticket. Scope to one attempt\n" +
		"with --attempt.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		if !root.Exists(id) {
			return fmt.Errorf("ticket %q not found under %s", id, root.Dir)
		}

		var results []audit.Result
		if attemptFlag != "" || os.Getenv("DRAIVER_ATTEMPT") != "" {
			att, err := resolveAttempt(root, id)
			if err != nil {
				return err
			}
			if !root.AttemptExists(id, att) {
				return fmt.Errorf("attempt %s/%s not found", id, att)
			}
			res, err := audit.VerifyAttempt(root, id, att)
			if err != nil {
				return err
			}
			results = []audit.Result{res}
		} else {
			results, err = audit.VerifyTicket(root, id)
			if err != nil {
				return err
			}
		}

		if len(results) == 0 {
			return fmt.Errorf("ticket %q has no attempts", id)
		}
		anyFail := false
		for _, res := range results {
			if res.OK {
				fmt.Fprintf(cmd.OutOrStdout(), "OK   %s/%s: %d events, chain intact\n", id, res.Attempt, res.Count)
			} else {
				anyFail = true
				fmt.Fprintf(cmd.OutOrStdout(), "FAIL %s/%s: %s\n", id, res.Attempt, res.Reason)
			}
		}
		if anyFail {
			return &exitError{code: ExitAuditFailed, msg: ""}
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(auditCmd)
}
