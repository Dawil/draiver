package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
)

var statusCmd = &cobra.Command{
	Use:   "status [TICKET]",
	Short: "Regenerate per-attempt state.md projections and print the board summary",
	Args:  cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		now := time.Now()

		attempts, err := gatherAttempts(root, args)
		if err != nil {
			return err
		}
		for _, a := range attempts {
			if err := writeState(root, a, now); err != nil {
				return err
			}
		}
		// Desiredness (for the Pending projection) must be computed over the whole
		// fleet + all edges even when the board is filtered to one ticket, since a
		// desired parent pulling a child in via `wants:` may live under another ticket.
		desired, err := computeDesired(root)
		if err != nil {
			return err
		}
		printBoard(cmd, root, attempts, desired)
		return nil
	},
}

// computeDesired derives the declaratively-desired attempt set across the whole
// root — the read-side of who the daemon would supervise (direct enable + enabled-
// via-parent down `wants:`). It is the "desired" input to the Pending projection.
func computeDesired(root store.Root) (map[project.Ref]bool, error) {
	all, err := project.LoadAll(root)
	if err != nil {
		return nil, err
	}
	edges, err := project.LoadAllEdges(root)
	if err != nil {
		return nil, err
	}
	return project.DeriveDesired(all, edges), nil
}

// gatherAttempts returns the attempts a status run covers: a single attempt when
// --attempt is set, all attempts of one ticket when a ticket is named, or every
// attempt across all tickets otherwise.
func gatherAttempts(root store.Root, args []string) ([]project.Attempt, error) {
	if len(args) == 0 {
		return project.LoadAll(root)
	}
	id := args[0]
	if !root.Exists(id) {
		return nil, fmt.Errorf("ticket %q not found under %s", id, root.Dir)
	}
	if attemptFlag != "" || os.Getenv("DRAIVER_ATTEMPT") != "" {
		att, err := resolveAttempt(root, id)
		if err != nil {
			return nil, err
		}
		if !root.AttemptExists(id, att) {
			return nil, fmt.Errorf("attempt %s/%s not found", id, att)
		}
		a, err := project.LoadAttempt(root, id, att)
		if err != nil {
			return nil, err
		}
		return []project.Attempt{a}, nil
	}
	ids, err := root.ListAttempts(id)
	if err != nil {
		return nil, err
	}
	var out []project.Attempt
	for _, aid := range ids {
		a, err := project.LoadAttempt(root, id, aid)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func writeState(root store.Root, a project.Attempt, now time.Time) error {
	if err := os.WriteFile(root.StatePath(a.Ticket, a.ID), project.RenderState(a, now), 0o644); err != nil {
		return fmt.Errorf("write state.md for %s/%s: %w", a.Ticket, a.ID, err)
	}
	return nil
}

func printBoard(cmd *cobra.Command, root store.Root, attempts []project.Attempt, desired map[project.Ref]bool) {
	out := cmd.OutOrStdout()
	var running, pending, done int
	var needsMe, review []project.Attempt
	for _, a := range attempts {
		// Pending is a read-time projection: a desired, log-Running attempt with no
		// live agent is waiting on a gate/activation, not working. Fill the runtime
		// bits here — desiredness from the fleet-wide set, liveness from the session
		// pid probe — rather than in the log-pure loader, so the count reflects the
		// fleet's real state at status time.
		a.Desired = desired[project.Ref{Ticket: a.Ticket, Attempt: a.ID}]
		a.Live = session.Alive(root, a.Ticket, a.ID)
		switch a.Control() {
		case project.Running:
			running++
		case project.Pending:
			pending++
		case project.Done:
			done++
		case project.NeedsMe:
			needsMe = append(needsMe, a)
		case project.Review:
			review = append(review, a)
		}
	}
	fmt.Fprintf(out, "Running: %d   Pending: %d   Needs me: %d   Review: %d   Done: %d\n",
		running, pending, len(needsMe), len(review), done)
	for _, a := range needsMe {
		fmt.Fprintf(out, "  [Needs me] %s/%s — %s (%d open)\n", a.Ticket, a.ID, a.Title, len(a.OpenEscalations))
	}
	for _, a := range review {
		fmt.Fprintf(out, "  [Review]   %s/%s — %s\n", a.Ticket, a.ID, a.Title)
	}
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
