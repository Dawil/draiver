package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/event"
)

var (
	logType       string
	logRefs       []int
	logArtefacts  []string
	logURLs       []string
	logLinks      []string
	logSupersedes []int
	logTags       []string
)

var logCmd = &cobra.Command{
	Use:   "log TICKET MESSAGE",
	Short: "Append a typed event (gotcha, decision, note, ...) to a ticket",
	Long: "log is the generic recorder. Use --type to name the event kind — e.g.\n" +
		"gotcha for something that bit you, decision for a choice and its alternatives,\n" +
		"note for context. Reference other events with --ref and blobs with --artefact.\n\n" +
		"When a fact turns out wrong, don't append a contradicting fact — supersede the\n" +
		"stale one with --supersedes <seq> (find its seq in `draiver recall`), so the\n" +
		"brief's index drops it from the live set. --tag attaches opaque retrieval labels\n" +
		"(reuse tags already visible in the index to avoid drift).",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, msg := args[0], args[1]
		if logType == "" {
			return fmt.Errorf("--type is required")
		}
		links, err := buildLinks(logURLs, logLinks)
		if err != nil {
			return err
		}
		e, att, err := appendEvent(id, event.Event{
			Type:       logType,
			Refs:       logRefs,
			Artefacts:  logArtefacts,
			Links:      links,
			Supersedes: logSupersedes,
			Tags:       logTags,
			Body:       msg,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "logged %s #%d on %s/%s\n", e.Type, e.Seq, id, att)
		return nil
	},
}

func init() {
	logCmd.Flags().StringVar(&logType, "type", "", "event type (gotcha|decision|note|...) (required)")
	logCmd.Flags().IntSliceVar(&logRefs, "ref", nil, "seq of an event this one references (repeatable)")
	logCmd.Flags().StringSliceVar(&logArtefacts, "artefact", nil, "path under artefacts/ this event references (repeatable)")
	logCmd.Flags().IntSliceVar(&logSupersedes, "supersedes", nil, "seq of an event this one retires from the live set (repeatable; must exist on this attempt)")
	logCmd.Flags().StringSliceVar(&logTags, "tag", nil, "opaque retrieval label (repeatable; no meaning to draiver)")
	addLinkFlags(logCmd, &logURLs, &logLinks)
	rootCmd.AddCommand(logCmd)
}
