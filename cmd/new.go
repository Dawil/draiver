package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"draiver/internal/attempt"
)

var (
	newTitle    string
	newProject  string
	newTeam     string
	newAssignee string
	newSpecFile string
	newTool     string
	newModel    string
)

var newCmd = &cobra.Command{
	Use:   "new TICKET",
	Short: "Create a ticket (spec.md) and its first attempt",
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
		if err := root.EnsureTicketDir(id); err != nil {
			return err
		}

		spec, err := buildSpec(id)
		if err != nil {
			return err
		}
		if err := os.WriteFile(root.SpecPath(id), spec, 0o644); err != nil {
			return fmt.Errorf("write spec: %w", err)
		}

		m, err := attempt.Create(root, id, attempt.New{
			Tool:  newTool,
			Model: newModel,
			Actor: resolveActor(),
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "created %s attempt %s at %s\n", id, m.ID, root.TicketDir(id))
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
	b.WriteString("<!-- Frontloaded design goes here. This file is the immutable input,\n")
	b.WriteString("     shared by every attempt; everything mutable lives in the log. -->\n")
	return []byte(b.String()), nil
}

func init() {
	newCmd.Flags().StringVar(&newTitle, "title", "", "human-readable ticket title")
	newCmd.Flags().StringVar(&newProject, "project", "", "project field")
	newCmd.Flags().StringVar(&newTeam, "team", "", "team field")
	newCmd.Flags().StringVar(&newAssignee, "assignee", "", "assignee field")
	newCmd.Flags().StringVar(&newSpecFile, "spec", "", "import spec.md from this file instead of scaffolding one")
	newCmd.Flags().StringVar(&newTool, "tool", "", "coding-agent tool for the first attempt (e.g. claude-code)")
	newCmd.Flags().StringVar(&newModel, "model", "", "model for the first attempt (e.g. opus-4.8)")
	rootCmd.AddCommand(newCmd)
}
