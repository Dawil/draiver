package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var titleCmd = &cobra.Command{
	Use:   "title TICKET TEXT",
	Short: "Set or update a ticket's spec.md title (metadata, outside the log)",
	Long: "Write TEXT into the ticket's spec.md `title:` frontmatter, adding a " +
		"frontmatter block if the spec has none. Reuses the same merge logic as " +
		"`new --spec` (injectTitle). The title lives outside the hash-chained log, " +
		"so this never affects `draiver audit`; titles are read live on every " +
		"board/brief render, so the change shows up immediately.",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		title := strings.TrimSpace(args[1])
		if title == "" {
			return fmt.Errorf("title must be non-empty")
		}
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		if !root.Exists(id) {
			return fmt.Errorf("ticket %q not found under %s", id, root.Dir)
		}
		specPath := root.SpecPath(id)
		data, err := os.ReadFile(specPath)
		if err != nil {
			return fmt.Errorf("read spec: %w", err)
		}
		if err := os.WriteFile(specPath, injectTitle(data, title), 0o644); err != nil {
			return fmt.Errorf("write spec: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "titled %s: %s\n", id, title)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(titleCmd)
}
