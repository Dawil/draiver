package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/wake"
)

// parkClear is the `--clear` flag: run the operator inverse (force-resolve the
// attempt's current wake source) instead of suspending the attempt (drv-031).
var parkClear bool

// wokenByType is the reconciler's spine-authored activation event type (drvctl-051),
// mirrored here as a local literal (the package convention — there is no central
// registry). `park --clear` of a `wants` source authors one by hand to admit a
// dormant coordinator, exactly as the reconciler would on a child doorbell.
const wokenByType = "woken-by"

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
		reason := ""
		if len(args) == 2 {
			reason = args[1]
		}

		root, err := resolveRoot()
		if err != nil {
			return err
		}
		// The `ticket[@attempt]` positional (shared across the CLI): the @attempt suffix
		// wins over --attempt/$DRAIVER_ATTEMPT, both must exist (drv-031).
		ticket, att, err := resolveTarget(root, args[0])
		if err != nil {
			return err
		}

		if parkClear {
			return runParkClear(cmd, root, ticket, att)
		}

		// The park guard: refuse to suspend an attempt nothing can wake. This runs
		// before the append so a refused park leaves no event behind (no state change).
		sources, err := wake.Pending(root, ticket, att)
		if err != nil {
			return err
		}
		if len(sources) == 0 {
			return fmt.Errorf(
				"park refused: %s/%s has no pending wake source — nothing could wake it, so it would sleep forever. "+
					"Register a waker first (a `wants:` child still in flight, or an async job that will land a result on this log) "+
					"and park only once it is confirmed running; otherwise `review` or `escalate` instead",
				ticket, att)
		}

		e, _, err := appendEventAt(root, ticket, att, event.Event{Type: "park", Body: parkBody(reason, sources)})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "parked %s/%s (seq %d) — Pending until woken by %s\n",
			ticket, att, e.Seq, wakeSummary(sources))
		return nil
	},
}

// runParkClear is the operator inverse of park (drv-031): it force-resolves the
// attempt's *current* wake source(s) now, so a parked attempt whose waker will never
// fire wakes (or becomes reachable) without a human hand-forging log events. It is
// general over wake-source kinds — it reads whatever `wake.Pending` finds and
// dispatches per `wake.Kind`, never a `draiver test` concern.
//
// With no pending source it is an idempotent no-op (the deliberate dual of park's
// no-source refusal): it prints a notice and exits 0, writing nothing. The events it
// does write carry the operator's own actor (resolveActor), so the log honestly
// records a human intervention rather than an `agent:*` verdict.
func runParkClear(cmd *cobra.Command, root store.Root, ticket, att string) error {
	out := cmd.OutOrStdout()

	sources, err := wake.Pending(root, ticket, att)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Fprintf(out, "nothing to clear: %s/%s has no pending wake source\n", ticket, att)
		return nil
	}

	for _, s := range sources {
		switch s.Kind {
		case wake.AsyncJob:
			// Land the terminal fail `test-result` answering the open launch marker,
			// reusing launchDetached's spawn-failure discipline so no dangling source is
			// left. Outcome "fail" guarantees the review gate never reads it as green.
			e, _, err := answerLaunchFailed(root, ticket, att, s.Ref, s.Rung, clearAsyncBody(s.Ref))
			if err != nil {
				return fmt.Errorf("park --clear: answer async-job launch #%d on %s/%s: %w", s.Ref, ticket, att, err)
			}
			fmt.Fprintf(out, "cleared async-job source on %s/%s — landed a fail `test-result` (seq %d) answering launch #%d; the reconciler will wake the attempt to iterate\n",
				ticket, att, e.Seq, s.Ref)

		case wake.Wants:
			// Author a manual reverse-`wants` activation on the coordinator's OWN log — a
			// `woken-by` past the latest park, which the reconciler's active phase reads as
			// "admit this coordinator to assess now", even though its children are not all
			// Done. It answers no specific child doorbell (operator force), so it carries no
			// WokenChild/WokenSeq and consumes nothing from the doorbell queue.
			e, _, err := appendEventAt(root, ticket, att, event.Event{
				Type: wokenByType,
				Body: clearWantsBody(),
			})
			if err != nil {
				return fmt.Errorf("park --clear: author %s on %s/%s: %w", wokenByType, ticket, att, err)
			}
			fmt.Fprintf(out, "cleared wants source on %s/%s — authored a `%s` activation (seq %d); the coordinator is admitted to assess now\n",
				ticket, att, wokenByType, e.Seq)

		default:
			// A later wake-source kind (e.g. a timer) must be an obvious compile-/run-time
			// gap, not a silent no-op that leaves the attempt asleep.
			return fmt.Errorf("park --clear: don't know how to clear source %q", s.Kind)
		}
	}
	return nil
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

// clearAsyncBody renders the terminal `test-result` an operator `park --clear` lands
// to answer an async-job launch. It is explicit that this is an operator intervention
// ending a wait, NOT a real test verdict — the board renders it as Markdown.
func clearAsyncBody(launchSeq int) string {
	return fmt.Sprintf("Operator `park --clear`: force-resolved the pending async-job wake source (launch #%d). **No climb ran** — this is not a test verdict; `Outcome: fail` only ends the wait so the attempt wakes to iterate. Relaunch `draiver test --detach && draiver park` if the detached runner is still wanted.", launchSeq)
}

// clearWantsBody renders the manual reverse-`wants` activation an operator
// `park --clear` authors to admit a dormant coordinator without waiting on a child
// doorbell.
func clearWantsBody() string {
	return "Operator `park --clear`: force-admitted this coordinator to assess now, without waiting on a child lifecycle doorbell. Authored by a human intervention, not the reconciler — children need not all be Done."
}

func init() {
	parkCmd.Flags().BoolVar(&parkClear, "clear", false,
		"operator inverse: force-resolve the attempt's current wake source now (land a terminal result / activation) so a parked attempt wakes without hand-forging log events")
	rootCmd.AddCommand(parkCmd)
}
