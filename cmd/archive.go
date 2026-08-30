package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
)

// archive/unarchive are board-membership verbs: they take an attempt off the board
// (or bring it back) without touching its lifecycle State — an `archive`/`unarchive`
// log event whose last occurrence wins (DeriveArchived, default un-archived). They
// are the CLI half of the symmetry drvweb-019 left open: the board's tick/cross wrote
// this event directly (the one webui write with no verb), while a human at a terminal
// had only the generic `log ... --type archive`, which cannot set the Outcome
// sentiment. These verbs write the same event, with the same Outcome and body copy,
// so a board- and a CLI-authored archive are indistinguishable in the log and to
// metrics. drvweb-020 then repoints handleArchive at this verb.

var (
	archiveAccepted  bool
	archiveAbandoned bool
)

var archiveCmd = &cobra.Command{
	Use:   "archive <ticket[@attempt]>",
	Short: "Take an attempt off the board (records the accepted/abandoned sentiment)",
	Long: "archive removes an attempt from the board without closing or reopening it — a\n" +
		"separate axis from lifecycle State. The archive carries a sentiment: --accepted\n" +
		"(the board's green tick — a finished attempt filed away) or --abandoned (the grey\n" +
		"cross — closed without success). With neither flag it mirrors the board and derives\n" +
		"the sentiment from State: accepted when the attempt is Done, else abandoned.\n" +
		"Archiving an already-archived attempt is a reported no-op.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		// resolveCtlTarget parses `ticket[@attempt]` (explicit @attempt wins, else
		// --attempt / $DRAIVER_ATTEMPT / latest) and errors — writing nothing — for a
		// missing ticket or attempt, matching the enable/disable/merge verbs.
		ticket, att, err := resolveCtlTarget(root, args[0])
		if err != nil {
			return err
		}
		a, err := project.LoadAttempt(root, ticket, att)
		if err != nil {
			return err
		}
		// Idempotency: archiving an already-archived attempt reports the state and
		// exits 0 without a second event, the CLI peer of the webui's canArchive
		// double-submit guard.
		if a.Archived {
			fmt.Fprintf(cmd.OutOrStdout(), "already archived: %s/%s (no-op)\n", ticket, att)
			return nil
		}
		// Default the sentiment from State like the board's archiveAction (Done →
		// accepted, else abandoned); the flags override. Mutual exclusion is enforced
		// by cobra before RunE, so at most one is set here.
		outcome := archiveOutcome(a.State)
		if archiveAccepted {
			outcome = "accepted"
		}
		if archiveAbandoned {
			outcome = "abandoned"
		}
		e, _, err := appendEventAt(root, ticket, att, event.Event{
			Type:    "archive",
			Outcome: outcome,
			Body:    archiveBody(outcome),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "archived %s/%s as %s (archive #%d)\n", ticket, att, outcome, e.Seq)
		return nil
	},
}

var unarchiveCmd = &cobra.Command{
	Use:   "unarchive <ticket[@attempt]>",
	Short: "Return an archived attempt to the board",
	Long: "unarchive brings an archived attempt back onto the board (DeriveArchived flips\n" +
		"on the later event, last-wins). Nothing is destroyed — the log is immutable — only\n" +
		"the derived board-membership bit flips. It carries no sentiment. Unarchiving an\n" +
		"attempt that is already on the board is a reported no-op.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		ticket, att, err := resolveCtlTarget(root, args[0])
		if err != nil {
			return err
		}
		a, err := project.LoadAttempt(root, ticket, att)
		if err != nil {
			return err
		}
		if !a.Archived {
			fmt.Fprintf(cmd.OutOrStdout(), "already on the board: %s/%s (no-op)\n", ticket, att)
			return nil
		}
		e, _, err := appendEventAt(root, ticket, att, event.Event{
			Type: "unarchive",
			Body: "Unarchived and returned to the board.",
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "unarchived %s/%s (unarchive #%d)\n", ticket, att, e.Seq)
		return nil
	},
}

// archiveOutcome derives the archive sentiment from lifecycle State, mirroring the
// board's archiveAction (internal/web): a finished (Done) attempt is accepted, any
// active one (Running/Pending/Needs me/Review) is abandoned. It maps to the exact
// Outcome strings the board writes so CLI- and board-authored archives are
// indistinguishable in the log and to metrics.
func archiveOutcome(s project.State) string {
	if s == project.Done {
		return "accepted"
	}
	return "abandoned"
}

// archiveBody is the human-readable body stamped on an archive event, keyed off the
// sentiment. It reuses verbatim the copy the webui's archiveBody writes
// (internal/web/web.go) so log prose is identical regardless of origin; the
// machine-readable truth is the event's Outcome field. drv-008 will fold this shared
// copy into the extracted library.
func archiveBody(outcome string) string {
	if outcome == "accepted" {
		return "Accepted and archived from the board."
	}
	return "Closed and archived from the board."
}

func init() {
	archiveCmd.Flags().BoolVar(&archiveAccepted, "accepted", false, "record the accepted sentiment (the board's green tick) regardless of State")
	archiveCmd.Flags().BoolVar(&archiveAbandoned, "abandoned", false, "record the abandoned sentiment (the board's grey cross) regardless of State")
	// A single archive is either accepted or abandoned, never both: cobra rejects the
	// pair before RunE, so the mutual-exclusion error writes nothing.
	archiveCmd.MarkFlagsMutuallyExclusive("accepted", "abandoned")
	rootCmd.AddCommand(archiveCmd, unarchiveCmd)
}
