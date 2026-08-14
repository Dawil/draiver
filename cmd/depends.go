package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Dawil/draiver/internal/project"
)

var (
	dependsWants    []string
	dependsAfter    []string
	dependsRequires []string
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
		"offending path.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		if len(dependsWants) == 0 && len(dependsAfter) == 0 && len(dependsRequires) == 0 {
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
		merged := project.Edges{
			Wants:    unionIDs(existing.Wants, dependsWants),
			After:    unionIDs(existing.After, dependsAfter),
			Requires: unionIDs(existing.Requires, dependsRequires),
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
		if len(dependsWants) > 0 {
			data = injectListField(data, "wants", merged.Wants)
		}
		if len(dependsAfter) > 0 {
			data = injectListField(data, "after", merged.After)
		}
		if len(dependsRequires) > 0 {
			data = injectListField(data, "requires", merged.Requires)
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

// injectListField sets key to a flow-style YAML list (`key: [a, b, c]`) in data's
// frontmatter, replacing any existing entry for key — inline or block form — and
// leaving every other line, comment, and the body untouched. It mirrors
// injectTitle's line surgery (rather than a full re-marshal) so the scaffold's
// commented hint lines survive. An empty values slice removes the key.
func injectListField(data []byte, key string, values []string) []byte {
	s := string(data)
	line := yamlListLine(key, values)

	if !strings.HasPrefix(s, "---\n") {
		if len(values) == 0 {
			return data
		}
		return []byte("---\n" + line + "\n---\n\n" + s)
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		// Opening fence with no close: treat as bodyless and prepend a block.
		if len(values) == 0 {
			return data
		}
		return []byte("---\n" + line + "\n---\n\n" + s)
	}
	lines := strings.Split(rest[:end], "\n")

	// Find the key's line and the extent of its value: the key line plus any
	// following block-sequence item lines (trimmed `- …`) that belong to it. This
	// spans both `key: [a]` (no continuation) and the multi-line block form at any
	// indentation, since we already hold the merged values and only need to replace.
	start, stop := -1, -1
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), key+":") {
			start = i
			stop = i + 1
			for stop < len(lines) {
				t := strings.TrimSpace(lines[stop])
				if strings.HasPrefix(t, "-") {
					stop++
					continue
				}
				break
			}
			break
		}
	}

	var out []string
	switch {
	case start >= 0 && len(values) == 0:
		out = append(out, lines[:start]...)
		out = append(out, lines[stop:]...)
	case start >= 0:
		out = append(out, lines[:start]...)
		out = append(out, line)
		out = append(out, lines[stop:]...)
	case len(values) == 0:
		return data
	default:
		out = append(lines, line)
	}
	return []byte("---\n" + strings.Join(out, "\n") + rest[end:])
}

// yamlListLine renders `key: [a, b, c]` — a single flow-style frontmatter line —
// marshalling via yaml.v3 so any id needing quoting is encoded safely, matching
// yamlLine's guarantee for scalars.
func yamlListLine(key string, values []string) string {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, v := range values {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: v})
	}
	m := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: key}, seq,
	}}
	out, err := yaml.Marshal(m)
	if err != nil {
		// Marshalling a string sequence does not fail; guard defensively.
		return key + ": [" + strings.Join(values, ", ") + "]"
	}
	return strings.TrimRight(string(out), "\n")
}

func init() {
	dependsCmd.Flags().StringSliceVar(&dependsWants, "wants", nil, "ticket ids this ticket wants (enable-propagation); repeatable, comma-separated")
	dependsCmd.Flags().StringSliceVar(&dependsAfter, "after", nil, "ticket ids to admit after they reach Review; repeatable, comma-separated")
	dependsCmd.Flags().StringSliceVar(&dependsRequires, "requires", nil, "ticket ids to admit after they reach Done; repeatable, comma-separated")
	rootCmd.AddCommand(dependsCmd)
}
