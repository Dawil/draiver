package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/project"
)

var inboxMine bool

var inboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "List unresolved escalations across all attempts of all tickets",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		attempts, err := project.LoadAll(root)
		if err != nil {
			return err
		}
		// The human inbox excludes a child whose escalation moved up to a pre-digest
		// parent (drvctl-050): the manager fields it, so it is not the human's to
		// resolve directly. Same fleet-wide move-up the board projects; under
		// passthrough (the default) the map is empty and every escalation shows.
		escalated, err := computeEscalated(root)
		if err != nil {
			return err
		}
		me := actorName(resolveActor())

		out := cmd.OutOrStdout()
		n := 0
		for _, a := range attempts {
			if inboxMine && !assigneeMatches(a.Assignee, me) {
				continue
			}
			if p := escalated[project.Ref{Ticket: a.Ticket, Attempt: a.ID}]; p != "" {
				continue // moved up to parent p — not in the human inbox
			}
			for _, e := range a.OpenEscalations {
				fmt.Fprintf(out, "%s/%s #%d — %s\n", a.Ticket, a.ID, e.Seq, firstLine(e.Body))
				n++
			}
		}
		if n == 0 {
			fmt.Fprintln(out, "inbox clear — no unresolved escalations")
		}
		return nil
	},
}

// actorName strips the "kind:" prefix from an actor string.
func actorName(actor string) string {
	if i := strings.IndexByte(actor, ':'); i >= 0 {
		return actor[i+1:]
	}
	return actor
}

func assigneeMatches(assignee, me string) bool {
	return assignee != "" && strings.EqualFold(assignee, me)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func init() {
	inboxCmd.Flags().BoolVar(&inboxMine, "mine", false, "only escalations on tickets assigned to me")
	rootCmd.AddCommand(inboxCmd)
}
