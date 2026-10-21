package cmd

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/recallmetric"
)

var recallStatsCmd = &cobra.Command{
	Use:   "recall-stats [ticket]",
	Short: "Aggregate recall instrumentation into per-ticket and per-fact recall counts",
	Long: "recall-stats folds the root-level recall-metrics sink `recall` writes one line\n" +
		"to per invocation (drv-036) into the two questions the instrumentation exists to\n" +
		"answer: how often each ticket is recalled, and how often each fact (ticket@attempt\n" +
		"#seq) is pulled. Both lists are sorted most-recalled-first, so the top of the\n" +
		"per-fact list is the belief carrying the most weight. With a ticket argument it\n" +
		"scopes to that ticket. It is a pure read over the on-disk sink — offline, no\n" +
		"daemon — and the sink itself stays greppable as the lower-tech path.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		var ticketFilter string
		if len(args) == 1 {
			ticketFilter = args[0]
		}

		recs, err := recallmetric.Read(root)
		if err != nil {
			return err
		}
		stats := recallmetric.Aggregate(recs, ticketFilter)

		out := cmd.OutOrStdout()
		if stats.Total == 0 {
			if ticketFilter != "" {
				fmt.Fprintf(out, "no recalls recorded for %s\n", ticketFilter)
			} else {
				fmt.Fprintln(out, "no recalls recorded yet")
			}
			return nil
		}

		fmt.Fprintf(out, "%d recall(s) recorded\n", stats.Total)

		fmt.Fprintln(out, "\nper-ticket:")
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, t := range stats.Tickets {
			fmt.Fprintf(tw, "  %s\t%d\n", t.Ticket, t.Count)
		}
		tw.Flush()

		fmt.Fprintln(out, "\nper-fact:")
		tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, f := range stats.Facts {
			fmt.Fprintf(tw, "  %s@%s#%d\t%d\n", f.Key.Ticket, f.Key.Attempt, f.Key.Seq, f.Count)
		}
		tw.Flush()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(recallStatsCmd)
}
