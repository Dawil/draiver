package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"draiver/internal/project"
	"draiver/internal/store"
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
		printBoard(cmd, attempts)
		return nil
	},
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

func printBoard(cmd *cobra.Command, attempts []project.Attempt) {
	out := cmd.OutOrStdout()
	var running, done int
	var needsMe, review []project.Attempt
	for _, a := range attempts {
		switch a.State {
		case project.Running:
			running++
		case project.Done:
			done++
		case project.NeedsMe:
			needsMe = append(needsMe, a)
		case project.Review:
			review = append(review, a)
		}
	}
	fmt.Fprintf(out, "Running: %d   Needs me: %d   Review: %d   Done: %d\n",
		running, len(needsMe), len(review), done)
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
