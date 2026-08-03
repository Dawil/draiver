// Package cmd wires the draiver CLI verbs — the shared human<->agent protocol —
// onto the append-only ticket log.
package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"draiver/internal/event"
	"draiver/internal/store"
	"draiver/internal/ticketlog"
)

const (
	// ExitEscalated signals that an escalation was raised: the agent must halt.
	// Distinct from a plain failure so a supervising loop can branch on
	// "blocked" versus "errored".
	ExitEscalated = 3
	// ExitAuditFailed signals a broken hash chain.
	ExitAuditFailed = 4
)

var (
	dataFlag  string
	actorFlag string
)

// exitError carries a specific process exit code out of a command.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

var rootCmd = &cobra.Command{
	Use:   "draiver",
	Short: "Coordination substrate for supervising AI coding agents at the ticket level",
	Long: "draiver externalizes a ticket's valuable context — spec, decisions, " +
		"escalations — into an append-only, hash-chained log on disk, so any fresh " +
		"agent can pick up a ticket and a human attends only to what needs them.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&dataFlag, "data", "", "data root (default $DRAIVER_DATA or ~/.draiver/data)")
	rootCmd.PersistentFlags().StringVar(&actorFlag, "actor", "", "actor as kind:name (default $DRAIVER_ACTOR or human:$USER)")
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	err := rootCmd.Execute()
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintln(os.Stderr, "draiver:", ee.msg)
		}
		return ee.code
	}
	fmt.Fprintln(os.Stderr, "draiver:", err)
	return 1
}

// resolveRoot resolves the data root from the --data flag / env / default.
func resolveRoot() (store.Root, error) {
	return store.Resolve(dataFlag)
}

// resolveActor resolves the acting identity: --actor, then $DRAIVER_ACTOR, then
// human:$USER.
func resolveActor() string {
	if actorFlag != "" {
		return actorFlag
	}
	if v := os.Getenv("DRAIVER_ACTOR"); v != "" {
		return v
	}
	u := os.Getenv("USER")
	if u == "" {
		u = "unknown"
	}
	return "human:" + u
}

// appendEvent resolves the root, stamps the actor, and appends the event.
func appendEvent(id string, e event.Event) (event.Event, error) {
	root, err := resolveRoot()
	if err != nil {
		return event.Event{}, err
	}
	if !root.Exists(id) {
		return event.Event{}, fmt.Errorf("ticket %q not found under %s", id, root.Dir)
	}
	if e.Actor == "" {
		e.Actor = resolveActor()
	}
	return ticketlog.Append(root, id, e)
}
