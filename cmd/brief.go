package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/brief"
)

var briefCmd = &cobra.Command{
	Use:   "brief <ticket[@attempt]>",
	Short: "Replay spec + an attempt's log into a context blob for a fresh agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		// Resolve the `ticket[@attempt]` address form through the shared parser the
		// rest of the CLI uses (log/recall/ctl/archive): an explicit @attempt wins,
		// else --attempt / $DRAIVER_ATTEMPT / the latest. This lets a brief address a
		// named attempt as <TICKET>@<slug> the same way recall/log do (drv-040).
		id, att, err := resolveTarget(root, args[0])
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
