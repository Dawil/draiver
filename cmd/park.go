package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/wake"
)

var parkCmd = &cobra.Command{
	Use:   "park TICKET [REASON]",
	Short: "Suspend the attempt to Pending until a wake source fires (a handoff, no human needed)",
	Long: "park ends the session's turn by dropping the attempt to Pending — enabled,\n" +
		"no live agent, self-resolving — without raising a human-facing escalation. It\n" +
		"is the third legitimate way to end a turn, alongside `review` (hand off for\n" +
		"ratification) and `escalate` (block on a human): use it when nothing needs you\n" +
		"right now and something will wake you later.\n" +
		"\n" +
		"park REFUSES (nonzero, no state change) when the attempt has no pending wake\n" +
		"source — there must be something that can wake it, or it would sleep forever.\n" +
		"A `wants:` coordinator mid-epic is woken by its children's lifecycle; the\n" +
		"canonical worker guard is `launch-async && draiver park` — park only once the\n" +
		"waker is confirmed running.\n" +
		"\n" +
		"A parked attempt's session is reaped (sessions are cattle) but its log and\n" +
		"attempt persist; a later wake resumes it from `brief`.",
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		reason := ""
		if len(args) == 2 {
			reason = args[1]
		}

		root, err := resolveRoot()
		if err != nil {
			return err
		}
		if !root.Exists(id) {
			return fmt.Errorf("ticket %q not found under %s", id, root.Dir)
		}
		att, err := resolveAttempt(root, id)
		if err != nil {
			return err
		}

		// The park guard: refuse to suspend an attempt nothing can wake. This runs
		// before the append so a refused park leaves no event behind (no state change).
		sources, err := wake.Pending(root, id, att)
		if err != nil {
			return err
		}
		if len(sources) == 0 {
			return fmt.Errorf(
				"park refused: %s/%s has no pending wake source — nothing could wake it, so it would sleep forever. "+
					"Register a waker first (a `wants:` child still in flight, or an async job that will land a result on this log) "+
					"and park only once it is confirmed running; otherwise `review` or `escalate` instead",
				id, att)
		}

		e, _, err := appendEventAt(root, id, att, event.Event{Type: "park", Body: parkBody(reason, sources)})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "parked %s/%s (seq %d) — Pending until woken by %s\n",
			id, att, e.Seq, wakeSummary(sources))
		return nil
	},
}

// parkBody renders the durable park record: the agent's optional reason followed
// by the wake sources that made the park legitimate — so a resuming agent (and
// the board) can see why the attempt suspended and what will wake it.
func parkBody(reason string, sources []wake.Source) string {
	var b strings.Builder
	if reason != "" {
		b.WriteString(reason)
		b.WriteString("\n\n")
	} else {
		b.WriteString("Parked: nothing needs me right now; suspending to Pending until a wake source fires.\n\n")
	}
	b.WriteString("Wake source(s):\n")
	for _, s := range sources {
		fmt.Fprintf(&b, "- `%s` — %s\n", s.Kind, s.Desc)
	}
	return b.String()
}

// wakeSummary is the one-line "woken by …" tail of the park confirmation.
func wakeSummary(sources []wake.Source) string {
	kinds := make([]string, len(sources))
	for i, s := range sources {
		kinds[i] = string(s.Kind)
	}
	return strings.Join(kinds, ", ")
}

func init() {
	rootCmd.AddCommand(parkCmd)
}
