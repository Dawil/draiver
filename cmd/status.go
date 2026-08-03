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
	Short: "Regenerate state.md projection(s) and print the board summary",
	Args:  cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		now := time.Now()

		var tickets []project.Ticket
		if len(args) == 1 {
			if !root.Exists(args[0]) {
				return fmt.Errorf("ticket %q not found under %s", args[0], root.Dir)
			}
			t, err := project.Load(root, args[0])
			if err != nil {
				return err
			}
			tickets = []project.Ticket{t}
		} else {
			tickets, err = project.LoadAll(root)
			if err != nil {
				return err
			}
		}

		for _, t := range tickets {
			if err := writeState(root, t, now); err != nil {
				return err
			}
		}
		printBoard(cmd, tickets)
		return nil
	},
}

func writeState(root store.Root, t project.Ticket, now time.Time) error {
	if err := os.WriteFile(root.StatePath(t.ID), project.RenderState(t, now), 0o644); err != nil {
		return fmt.Errorf("write state.md for %s: %w", t.ID, err)
	}
	return nil
}

func printBoard(cmd *cobra.Command, tickets []project.Ticket) {
	out := cmd.OutOrStdout()
	var running, done int
	var needsMe, review []project.Ticket
	for _, t := range tickets {
		switch t.State {
		case project.Running:
			running++
		case project.Done:
			done++
		case project.NeedsMe:
			needsMe = append(needsMe, t)
		case project.Review:
			review = append(review, t)
		}
	}
	fmt.Fprintf(out, "Running: %d   Needs me: %d   Review: %d   Done: %d\n",
		running, len(needsMe), len(review), done)
	for _, t := range needsMe {
		fmt.Fprintf(out, "  [Needs me] %s — %s (%d open)\n", t.ID, t.Title, len(t.OpenEscalations))
	}
	for _, t := range review {
		fmt.Fprintf(out, "  [Review]   %s — %s\n", t.ID, t.Title)
	}
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
