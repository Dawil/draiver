// Package brief replays a ticket's spec and one attempt's log into a single
// context blob that cold-starts a fresh agent. If brief isn't enough to resume,
// the design is leaking state.
package brief

import (
	"fmt"
	"os"
	"strings"

	"github.com/Dawil/draiver/internal/kb"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
)

// The standing how-to-work protocol (cold-start, log discipline, the Markdown-
// when-logging nudge, escalate/review) no longer rides here in the brief. It is
// byte-invariant across every ticket, so it moved *above the wall* into the
// coding-agent's system-prompt append (internal/handbook, wired by drvctl-032's
// append_system_prompt_file), where it caches once per repo instead of paying a
// cold write on every ticket's cold-start (docs/prompt-caching.md, rung E). The
// brief now carries only the per-ticket half: the spec and the attempt log.

// Build assembles the brief for one attempt on a ticket.
func Build(root store.Root, ticket, id string) (string, error) {
	if !root.Exists(ticket) {
		return "", fmt.Errorf("ticket %q not found under %s", ticket, root.Dir)
	}
	if !root.AttemptExists(ticket, id) {
		return "", fmt.Errorf("attempt %s/%s not found", ticket, id)
	}
	a, err := project.LoadAttempt(root, ticket, id)
	if err != nil {
		return "", err
	}

	// The knowledge fold (drv-026): the brief pushes only a bounded *index* of
	// currently-live facts, not the whole log. Full bodies and supersession history
	// are pulled on demand via `draiver recall`.
	view := kb.Fold(a.Events)

	var b strings.Builder
	fmt.Fprintf(&b, "# BRIEF %s / attempt %s — %s\n", a.Ticket, a.ID, a.Title)
	fmt.Fprintf(&b, "State: %s", a.State)
	if a.Tool != "" {
		fmt.Fprintf(&b, " | Tool: %s", a.Tool)
	}
	if a.Model != "" {
		fmt.Fprintf(&b, " | Model: %s", a.Model)
	}
	if a.Repo != "" {
		fmt.Fprintf(&b, " | Repo: %s", a.Repo)
	}
	if a.Assignee != "" {
		fmt.Fprintf(&b, " | Assignee: %s", a.Assignee)
	}
	b.WriteString("\n\n")

	b.WriteString("## Spec\n\n")
	b.WriteString(specBody(root.SpecPath(ticket)))
	b.WriteString("\n\n")

	// ## Current understanding — the index: one line per live fact, in seq order.
	// Superseded bodies, resolved escalations, and historical events are absent; the
	// index scales with the live-fact count, not the total event count.
	live := view.Live()
	fmt.Fprintf(&b, "## Current understanding (%d live facts)\n\n", len(live))
	for _, f := range live {
		fmt.Fprintf(&b, "- %s\n", factLine(f))
	}
	b.WriteString("\n")

	if open := view.LiveEscalations(); len(open) > 0 {
		b.WriteString("## Open escalations (need a human)\n\n")
		for _, f := range open {
			fmt.Fprintf(&b, "- #%d: %s\n", f.Seq, f.Headline)
		}
		b.WriteString("\n")
	}

	b.WriteString("Run `draiver recall <seq>` for a fact's full body, " +
		"or `draiver recall --chain <seq>` for its history. The raw log is not shown here.\n")
	return b.String(), nil
}

// factLine renders one index entry: `#<seq> <type> [tags] — <headline>`. Tags are
// shown only when present; they are an opaque retrieval aid, so their order is the
// author's and the fold never interprets them.
func factLine(f kb.Fact) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%d %s", f.Seq, f.Type)
	if len(f.Tags) > 0 {
		fmt.Fprintf(&sb, " [%s]", strings.Join(f.Tags, " "))
	}
	fmt.Fprintf(&sb, " — %s", f.Headline)
	return sb.String()
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
