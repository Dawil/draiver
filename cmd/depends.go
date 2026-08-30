package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/repo"
)

var (
	dependsWants    []string
	dependsAfter    []string
	dependsRequires []string
	dependsSet      bool
)

var dependsCmd = &cobra.Command{
	Use:   "depends TICKET --wants … --after … --requires …",
	Short: "Author dependency edges into a ticket's spec.md frontmatter",
	Long: "Merge `wants:`/`after:`/`requires:` ticket-id edges into TICKET's spec.md " +
		"frontmatter. Each flag is repeatable and comma-separated, and adds to (never " +
		"replaces) the existing set. Like `draiver title`, these edges are metadata " +
		"outside the hash-chained log, so this never affects `draiver audit`.\n\n" +
		"Semantics (see docs/capabilities-and-supervision.md): wants = enable-" +
		"propagation, after = gate on Review, requires = gate on Done. The three form " +
		"a DAG; a self-edge or any edge that would close a cycle is refused with the " +
		"offending path.\n\n" +
		"With --set, each relation you pass *replaces* its existing set rather than " +
		"adding to it (a blank value clears that relation); relations you omit are left " +
		"untouched. This is the whole-set edit the web UI shells.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		// In --set mode a relation counts as touched when its flag was passed, even
		// with a blank value (a deliberate "clear"); in the default add mode a flag
		// only matters when it carries ids. Either way at least one relation must be
		// named, or there is nothing to write.
		touched := func(name string, vals []string) bool {
			if dependsSet {
				return cmd.Flags().Changed(name)
			}
			return len(vals) > 0
		}
		if !touched("wants", dependsWants) && !touched("after", dependsAfter) && !touched("requires", dependsRequires) {
			return fmt.Errorf("nothing to author: pass at least one of --wants/--after/--requires")
		}
		root, err := resolveRoot()
		if err != nil {
			return err
		}
		if !root.Exists(id) {
			return fmt.Errorf("ticket %q not found under %s", id, root.Dir)
		}

		// A ticket cannot depend on itself under any relation — refused before the
		// graph is even built, with a message that names the field.
		for _, spec := range []struct {
			rel  string
			vals []string
		}{{"wants", dependsWants}, {"after", dependsAfter}, {"requires", dependsRequires}} {
			for _, v := range spec.vals {
				if strings.TrimSpace(v) == id {
					return fmt.Errorf("self-edge refused: %s cannot depend on itself (%s)", id, spec.rel)
				}
			}
		}

		existing, err := project.LoadEdges(root, id)
		if err != nil {
			return err
		}
		// pick resolves one relation's final set: in --set mode a passed flag
		// replaces (its cleaned values, possibly empty) while an omitted flag keeps
		// the existing set; in the default mode every flag unions into the existing
		// set. cleanIDs and unionIDs share the trim/dedup/order discipline.
		pick := func(name string, existingVals, flagVals []string) []string {
			if dependsSet {
				if cmd.Flags().Changed(name) {
					return repo.CleanIDs(flagVals)
				}
				return existingVals
			}
			return unionIDs(existingVals, flagVals)
		}
		merged := project.Edges{
			Wants:    pick("wants", existing.Wants, dependsWants),
			After:    pick("after", existing.After, dependsAfter),
			Requires: pick("requires", existing.Requires, dependsRequires),
		}

		// Build the graph the edit *would* produce — every other ticket's edges as
		// they stand, with this ticket's replaced by the merged set — and refuse the
		// edit if it closes a cycle across the union of all three relations.
		all, err := project.LoadAllEdges(root)
		if err != nil {
			return err
		}
		all[id] = merged
		if path := project.CheckCycle(all); path != nil {
			return fmt.Errorf("edge refused: would create a dependency cycle: %s", project.FormatCyclePath(path))
		}

		// Merge only the touched fields into the frontmatter, preserving the rest of
		// the spec (body, comments, untouched keys) via the same line-surgery path as
		// `injectTitle`.
		specPath := root.SpecPath(id)
		data, err := os.ReadFile(specPath)
		if err != nil {
			return fmt.Errorf("read spec: %w", err)
		}
		if touched("wants", dependsWants) {
			data = repo.InjectListField(data, "wants", merged.Wants)
		}
		if touched("after", dependsAfter) {
			data = repo.InjectListField(data, "after", merged.After)
		}
		if touched("requires", dependsRequires) {
			data = repo.InjectListField(data, "requires", merged.Requires)
		}
		if err := os.WriteFile(specPath, data, 0o644); err != nil {
			return fmt.Errorf("write spec: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "edges on %s: wants=%s after=%s requires=%s\n",
			id, fmtList(merged.Wants), fmtList(merged.After), fmtList(merged.Requires))
		return nil
	},
}

// unionIDs appends add's ids to existing, trimming whitespace and dropping blanks
// and duplicates while preserving first-seen order — the "author an edge" merge:
// each invocation adds to the set rather than replacing it.
func unionIDs(existing, add []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, group := range [][]string{existing, add} {
		for _, v := range group {
			v = strings.TrimSpace(v)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func fmtList(v []string) string {
	if len(v) == 0 {
		return "[]"
	}
	return "[" + strings.Join(v, ",") + "]"
}

func init() {
	dependsCmd.Flags().StringSliceVar(&dependsWants, "wants", nil, "ticket ids this ticket wants (enable-propagation); repeatable, comma-separated")
	dependsCmd.Flags().StringSliceVar(&dependsAfter, "after", nil, "ticket ids to admit after they reach Review; repeatable, comma-separated")
	dependsCmd.Flags().StringSliceVar(&dependsRequires, "requires", nil, "ticket ids to admit after they reach Done; repeatable, comma-separated")
	dependsCmd.Flags().BoolVar(&dependsSet, "set", false, "replace each given relation's set instead of adding to it (a blank value clears that relation); the whole-set edit the web UI shells")
	rootCmd.AddCommand(dependsCmd)
}
