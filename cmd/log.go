package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"draiver/internal/event"
)

var (
	logType      string
	logRefs      []int
	logArtefacts []string
)

var logCmd = &cobra.Command{
	Use:   "log TICKET MESSAGE",
	Short: "Append a typed event (gotcha, decision, note, ...) to a ticket",
	Long: "log is the generic recorder. Use --type to name the event kind — e.g.\n" +
		"gotcha for something that bit you, decision for a choice and its alternatives,\n" +
		"note for context. Reference other events with --ref and blobs with --artefact.",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, msg := args[0], args[1]
		if logType == "" {
			return fmt.Errorf("--type is required")
		}
		e, err := appendEvent(id, event.Event{
			Type:      logType,
			Refs:      logRefs,
			Artefacts: logArtefacts,
			Body:      msg,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "logged %s #%d on %s\n", e.Type, e.Seq, id)
		return nil
	},
}

func init() {
	logCmd.Flags().StringVar(&logType, "type", "", "event type (gotcha|decision|note|...) (required)")
	logCmd.Flags().IntSliceVar(&logRefs, "ref", nil, "seq of an event this one references (repeatable)")
	logCmd.Flags().StringSliceVar(&logArtefacts, "artefact", nil, "path under artefacts/ this event references (repeatable)")
	rootCmd.AddCommand(logCmd)
}
