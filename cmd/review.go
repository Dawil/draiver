package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/event"
)

var (
	reviewURLs  []string
	reviewLinks []string
)

var reviewCmd = &cobra.Command{
	Use:   "review TICKET [CLAIM]",
	Short: "Claim a ticket is done (a claim, not a fact, for a human to verify)",
	Long: "review claims the ticket is complete for a human to verify. Attach the\n" +
		"review link — the draft PR, merge request, or diff URL where the change can\n" +
		"be seen — with --url (rel defaults to pr) or --link rel=uri for other rels.\n" +
		"\n" +
		"Push your branch to a git remote first so the link resolves: review only\n" +
		"logs and validates the link, it does not push or open a PR. With one remote\n" +
		"use it; with several, push to the config's primary_remote. Prefer a compare\n" +
		"URL (--link compare=<base>/compare/<main>...<branch>) when there is no PR.",
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		body := "Agent claims the ticket is complete; ready for review."
		if len(args) == 2 {
			body = args[1]
		}
		links, err := buildLinks(reviewURLs, reviewLinks)
		if err != nil {
			return err
		}
		e, att, err := appendEvent(id, event.Event{Type: "review", Body: body, Links: links})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "review claimed on %s/%s (seq %d)\n", id, att, e.Seq)
		return nil
	},
}

func init() {
	addLinkFlags(reviewCmd, &reviewURLs, &reviewLinks)
	rootCmd.AddCommand(reviewCmd)
}
