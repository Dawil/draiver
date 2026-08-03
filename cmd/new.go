package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"draiver/internal/event"
	"draiver/internal/ticketlog"
)

var (
	newTitle    string
	newProject  string
	newTeam     string
	newAssignee string
	newSpecFile string
)

var newCmd = &cobra.Command{
	Use:   "new TICKET",
	Short: "Create a ticket folder, spec.md, and the genesis event",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		if root.Exists(id) {
			return fmt.Errorf("ticket %q already exists", id)
		}
		if err := root.EnsureTicketDirs(id); err != nil {
			return err
		}

		spec, err := buildSpec(id)
		if err != nil {
			return err
		}
		if err := os.WriteFile(root.SpecPath(id), spec, 0o644); err != nil {
			return fmt.Errorf("write spec: %w", err)
		}

		body := fmt.Sprintf("Ticket created: %s", newTitle)
		if newTitle == "" {
			body = "Ticket created."
		}
		e, err := ticketlog.Append(root, id, event.Event{
			Type:  "created",
			Actor: resolveActor(),
			Body:  body,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "created %s (seq %d) at %s\n", id, e.Seq, root.TicketDir(id))
		return nil
	},
}

func buildSpec(id string) ([]byte, error) {
	if newSpecFile != "" {
		return os.ReadFile(newSpecFile)
	}
	title := newTitle
	if title == "" {
		title = id
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", id)
	fmt.Fprintf(&b, "title: %s\n", title)
	fmt.Fprintf(&b, "project: %s\n", newProject)
	fmt.Fprintf(&b, "team: %s\n", newTeam)
	fmt.Fprintf(&b, "assignee: %s\n", newAssignee)
	fmt.Fprintf(&b, "created: %s\n", time.Now().UTC().Format(time.RFC3339))
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n", title)
	b.WriteString("<!-- Frontloaded design goes here. This file is the immutable input;\n")
	b.WriteString("     everything mutable lives in the append-only log. -->\n")
	return []byte(b.String()), nil
}

func init() {
	newCmd.Flags().StringVar(&newTitle, "title", "", "human-readable ticket title")
	newCmd.Flags().StringVar(&newProject, "project", "", "project field")
	newCmd.Flags().StringVar(&newTeam, "team", "", "team field")
	newCmd.Flags().StringVar(&newAssignee, "assignee", "", "assignee field")
	newCmd.Flags().StringVar(&newSpecFile, "spec", "", "import spec.md from this file instead of scaffolding one")
	rootCmd.AddCommand(newCmd)
}
