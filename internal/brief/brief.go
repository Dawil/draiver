// Package brief replays a ticket's spec and log into a single context blob that
// cold-starts a fresh agent. If brief isn't enough to resume, the design is
// leaking state.
package brief

import (
	"fmt"
	"os"
	"strings"
	"time"

	"draiver/internal/event"
	"draiver/internal/project"
	"draiver/internal/store"
)

// Build assembles the brief for a ticket.
func Build(root store.Root, id string) (string, error) {
	if !root.Exists(id) {
		return "", fmt.Errorf("ticket %q not found under %s", id, root.Dir)
	}
	t, err := project.Load(root, id)
	if err != nil {
		return "", err
	}

	// Map each escalation seq to its resolution, for inline pairing.
	resolutionOf := map[int]event.Event{}
	for _, e := range t.Events {
		if e.Type == "resolution" {
			for _, r := range e.Refs {
				resolutionOf[r] = e
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# BRIEF %s — %s\n", t.ID, t.Title)
	fmt.Fprintf(&b, "State: %s", t.State)
	if t.Assignee != "" {
		fmt.Fprintf(&b, " | Assignee: %s", t.Assignee)
	}
	b.WriteString("\n\n")

	b.WriteString("## Spec\n\n")
	b.WriteString(specBody(root.SpecPath(id)))
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "## Log (%d events)\n\n", len(t.Events))
	for _, e := range t.Events {
		if e.Type == "resolution" {
			continue // shown inline under its escalation
		}
		fmt.Fprintf(&b, "### #%d %s — %s — %s\n", e.Seq, e.Type, e.Actor, e.TS.UTC().Format(time.RFC3339))
		if body := strings.TrimSpace(e.Body); body != "" {
			b.WriteString(body)
			b.WriteString("\n")
		}
		for _, a := range e.Artefacts {
			fmt.Fprintf(&b, "[artefact: %s]\n", a)
		}
		if e.Type == "escalation" {
			if res, ok := resolutionOf[e.Seq]; ok {
				fmt.Fprintf(&b, "→ RESOLVED by #%d (%s): %s\n", res.Seq, res.Actor, strings.TrimSpace(res.Body))
			} else {
				b.WriteString("→ UNRESOLVED — blocked here.\n")
			}
		}
		b.WriteString("\n")
	}

	if len(t.OpenEscalations) > 0 {
		b.WriteString("## Open escalations (need a human)\n\n")
		for _, e := range t.OpenEscalations {
			fmt.Fprintf(&b, "- #%d: %s\n", e.Seq, strings.TrimSpace(e.Body))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// specBody returns the spec markdown with its identity frontmatter stripped.
func specBody(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "(no spec.md)"
	}
	s := string(data)
	if strings.HasPrefix(s, "---\n") {
		rest := s[len("---\n"):]
		if i := strings.Index(rest, "\n---\n"); i >= 0 {
			return strings.TrimSpace(rest[i+len("\n---\n"):])
		}
	}
	return strings.TrimSpace(s)
}
