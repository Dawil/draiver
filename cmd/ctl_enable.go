package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// enable/disable are the supervision gate the daemon (`ctl up`) honours: only an
// attempt that is both Running and enabled joins the fleet the loop auto-spawns a
// session into. The bit is stored as an `enable`/`disable` log event — in the hash
// chain like every other, so it is durable and replays through `brief`. The
// default is disabled: merely being in Running is not consent to spawn a live
// agent + worktree, so a repo full of Running tickets stays quiet until each is
// explicitly opted in. This is systemd's enable/disable: in-fleet vs parked.

var ctlEnableCmd = &cobra.Command{
	Use:   "enable <ticket[@attempt]>",
	Short: "Opt an attempt into daemon supervision (`ctl up` will spawn a session for it)",
	Long: "enable records that an attempt should join the supervised fleet: once it is " +
		"both Running and enabled, `ctl up` brings up a session for it in an isolated " +
		"worktree. Enablement is stored as a log event (in the hash chain, replayed by " +
		"brief). The default is disabled — being in Running alone never auto-spawns an agent.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if ctlEnableNow {
			return enableNow(cmd, args[0])
		}
		return setEnabled(cmd, args[0], true)
	},
}

// ctlEnableNow backs `enable --now`: after recording the durable enable, hand the
// attempt to a running `ctl up` to bring up immediately (requires one). It is the
// persistent counterpart of `start` — enable survives a daemon restart where a
// bare `start`'s transient marker is swept.
var ctlEnableNow bool

// enableNow implements `enable --now`: it requires a live `ctl up` up front (so a
// no-daemon invocation persists nothing), records the durable enable event, and
// reports the handoff. The enable bit makes the attempt desired, so the daemon
// brings it up on its next tick — no transient marker needed.
func enableNow(cmd *cobra.Command, arg string) error {
	r, ticket, att, err := newCtlTarget(arg)
	if err != nil {
		return err
	}
	res, err := r.EnableNow(ticket, att)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"enabled %s/%s and handed it to draiverctld (pid %d) — supervision persists and it will come up shortly; watch with `ctl logs -f %s@%s` (enable #%d)\n",
		ticket, att, res.ControllerPID, ticket, att, res.Seq)
	return nil
}

var ctlDisableCmd = &cobra.Command{
	Use:   "disable <ticket[@attempt]>",
	Short: "Park an attempt: `ctl up` will not spawn a session for it (retires a live one)",
	Long: "disable records that an attempt should leave the supervised fleet. `ctl up` " +
		"stops auto-spawning a session for it, and retires one it currently supervises " +
		"(the attempt itself is untouched — a human can still work it by hand or re-enable " +
		"it later). Like enable, it is a log event in the hash chain.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setEnabled(cmd, args[0], false)
	},
}

// setEnabled appends an enable/disable lifecycle event to the resolved attempt.
// It targets a `ticket[@attempt]` exactly like the other client verbs — an
// explicit @attempt suffix wins, else --attempt / $DRAIVER_ATTEMPT / the latest.
func setEnabled(cmd *cobra.Command, arg string, enable bool) error {
	root, err := resolveRoot()
	if err != nil {
		return err
	}
	ticket, att, err := resolveCtlTarget(root, arg)
	if err != nil {
		return err
	}
	typ, verb := "disable", "disabled"
	if enable {
		typ, verb = "enable", "enabled"
	}
	e, err := ticketlog.Append(root, ticket, att, event.Event{
		Type:  typ,
		Actor: resolveActor(),
		Body:  fmt.Sprintf("Supervision %s for %s/%s.", verb, ticket, att),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s/%s (%s #%d)\n", verb, ticket, att, typ, e.Seq)
	return nil
}

func init() {
	// --now is the imperative add-on: enable persists as always, and additionally
	// the attempt is handed to the running supervisor to come up immediately. It
	// requires a live `ctl up` (unlike bare enable, which is purely declarative and
	// takes effect whenever a daemon next runs). Only on enable — meaningless on disable.
	ctlEnableCmd.Flags().BoolVar(&ctlEnableNow, "now", false, "also bring the attempt up immediately via the running `ctl up` (requires one; persists like enable, unlike the transient `start`)")
	ctlCmd.AddCommand(ctlEnableCmd, ctlDisableCmd)
}
