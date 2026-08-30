package repo

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/project"
)

// Provenance is a partial update to an attempt's attempt.md metadata: a nil field
// is left untouched, a non-nil one is written. Base-branch defaulting (an empty
// --base → the repo's current branch) is a git concern the CLI resolves before
// calling, so the values here are taken as given.
type Provenance struct {
	Repo  *string
	Base  *string
	Tool  *string
	Model *string
}

// SetProvenance applies a partial provenance update to an attempt's attempt.md and
// reports whether anything changed. attempt.md's provenance is static metadata, not
// a hash-chained log event, so this appends nothing to the log and never affects
// `draiver audit`; it is the shared write behind the CLI's `attempt set` and the
// webui's provenance panel. With no field set it writes nothing and returns
// changed=false, leaving the "nothing to set" framing to the caller.
func (r *Repo) SetProvenance(ticket, att string, p Provenance) (bool, error) {
	meta, err := attempt.LoadMeta(r.root, ticket, att)
	if err != nil {
		return false, err
	}
	changed := false
	if p.Repo != nil {
		meta.Repo = *p.Repo
		changed = true
	}
	if p.Base != nil {
		meta.Base = *p.Base
		changed = true
	}
	if p.Tool != nil {
		meta.Tool = *p.Tool
		changed = true
	}
	if p.Model != nil {
		meta.Model = *p.Model
		changed = true
	}
	if !changed {
		return false, nil
	}
	if err := attempt.WriteMeta(r.root, meta); err != nil {
		return false, err
	}
	return true, nil
}

// SetEdges replaces a ticket's wants/after/requires edge sets in spec.md
// frontmatter — the whole-set edit the webui performs and the CLI's `depends --set`
// path. Each relation's ids are cleaned (trimmed, de-duped, first-seen order). It
// refuses a self-edge or any edge that would close a dependency cycle across the
// fleet, writing nothing on refusal; edges are metadata outside the hash-chained
// log, so this never affects `draiver audit`. All three relations are written (a
// blank relation clears that key), matching the WYSIWYG panel.
func (r *Repo) SetEdges(ticket string, e project.Edges) error {
	merged := project.Edges{
		Wants:    CleanIDs(e.Wants),
		After:    CleanIDs(e.After),
		Requires: CleanIDs(e.Requires),
	}
	// A ticket cannot depend on itself under any relation — refused before the graph
	// is built, naming the field, exactly like the CLI's add path.
	for _, rel := range []struct {
		name string
		vals []string
	}{{"wants", merged.Wants}, {"after", merged.After}, {"requires", merged.Requires}} {
		for _, v := range rel.vals {
			if v == ticket {
				return fmt.Errorf("self-edge refused: %s cannot depend on itself (%s)", ticket, rel.name)
			}
		}
	}
	// Build the graph the edit would produce — every other ticket's edges as they
	// stand, this ticket's replaced by the merged set — and refuse if it closes a
	// cycle across the union of all three relations.
	all, err := project.LoadAllEdges(r.root)
	if err != nil {
		return err
	}
	all[ticket] = merged
	if path := project.CheckCycle(all); path != nil {
		return fmt.Errorf("edge refused: would create a dependency cycle: %s", project.FormatCyclePath(path))
	}
	data, err := os.ReadFile(r.root.SpecPath(ticket))
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}
	data = InjectListField(data, "wants", merged.Wants)
	data = InjectListField(data, "after", merged.After)
	data = InjectListField(data, "requires", merged.Requires)
	if err := os.WriteFile(r.root.SpecPath(ticket), data, 0o644); err != nil {
		return fmt.Errorf("write spec: %w", err)
	}
	return nil
}

// CleanIDs trims whitespace and drops blanks and duplicates from an id list,
// preserving first-seen order. An all-blank input yields an empty slice — the
// "clear this relation" signal. It is the shared trim/dedup discipline behind both
// the `depends` verb's add/replace modes and the webui's edge editor.
func CleanIDs(vals []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// InjectListField sets key to a flow-style YAML list (`key: [a, b, c]`) in data's
// frontmatter, replacing any existing entry for key — inline or block form — and
// leaving every other line, comment, and the body untouched. It mirrors the title
// line surgery (rather than a full re-marshal) so a scaffold's commented hint lines
// survive. An empty values slice removes the key.
func InjectListField(data []byte, key string, values []string) []byte {
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
// marshalling via yaml.v3 so any id needing quoting is encoded safely.
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
