package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/repo"
)

// supervision sets a Capability's per-ticket coordinator supervision mode
// (drvctl-042) — the dial on reverse-`wants:` activation. Like enable/disable it is
// a mutable control setting recorded as a log event (the mode rides on the event's
// Outcome field, the same multi-flavour mechanism `archive` uses), so it is durable
// in the hash chain and replays through `brief`. It overrides the global/project
// default in config; with no override recorded a coordinator inherits that default,
// which itself defaults to passthrough (the floor).
//
//   - passthrough — a sub-ticket escalation goes straight to Needs-me; no
//     coordinator wakes. The Capability is a Pending shell until its own final
//     review.
//   - pre-digest — a sub-ticket escalation wakes the coordinator (reverse-`wants:`
//     activation); it assesses across children and posts one consolidated
//     escalate-and-recommend for a human to ratify.
var ctlSupervisionCmd = &cobra.Command{
	Use:   "supervision <ticket[@attempt]> <" + strings.Join(project.SupervisionModes(), "|") + ">",
	Short: "Set a Capability's coordinator supervision mode (per-ticket override of the config default)",
	Long: "supervision records the coordinator supervision mode for a Capability — the dial on " +
		"reverse-`wants:` activation. In passthrough (the floor) a sub-ticket escalation goes " +
		"straight to Needs-me and no coordinator wakes; in pre-digest the escalation wakes the " +
		"coordinator to assess across children and post one consolidated recommendation for a human " +
		"to ratify. It is a log event (in the hash chain, replayed by brief) and overrides the " +
		"global default set by config's default_supervision.",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setSupervision(cmd, args[0], args[1])
	},
}

// setSupervision appends a `supervision` control event to the resolved attempt,
// validating the mode first so a typo is rejected at the edge rather than recorded
// as an unusable override. It targets a `ticket[@attempt]` exactly like the other
// client verbs — an explicit @attempt suffix wins, else --attempt / $DRAIVER_ATTEMPT
// / the latest.
func setSupervision(cmd *cobra.Command, arg, modeArg string) error {
	mode, err := project.ParseSupervision(modeArg)
	if err != nil {
		return err
	}
	root, err := resolveRoot()
	if err != nil {
		return err
	}
	ticket, att, err := resolveCtlTarget(root, arg)
	if err != nil {
		return err
	}
	e, err := repo.New(root, resolveActor()).Supervision(ticket, att, mode)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "supervision %s for %s/%s (supervision #%d)\n", mode, ticket, att, e.Seq)
	return nil
}

func init() {
	ctlCmd.AddCommand(ctlSupervisionCmd)
}
