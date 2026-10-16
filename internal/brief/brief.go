// Package brief replays a ticket's spec and one attempt's log into a single
// context blob that cold-starts a fresh agent. If brief isn't enough to resume,
// the design is leaking state.
package brief

import (
	"fmt"
	"os"
	"strings"

	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/kb"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
)

// wokenByType mirrors reconcile's spine-authored activation event type
// (drvctl-051). A `woken-by <child> #<seq>` event is appended to a coordinator's
// *own* log at wake time; the brief reads the latest one as its wake-reason tail
// (one source, three consumers — dedup, brief, board). Kept as a local const
// rather than importing reconcile (the daemon package) into this read-only
// assembler.
const wokenByType = "woken-by"

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

	// Manager-hood is a value on one axis, not a type (docs/manager-as-worker.md):
	// a ticket that `wants:` children is a coordinator, and must be *told* it is one
	// — otherwise it receives a byte-identical worker brief and defaults to
	// implementing its own spec, the one thing a manager must not do. A ticket with
	// no `wants:` edges falls through to today's worker brief unchanged.
	edges, err := project.LoadEdges(root, ticket)
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

	// Manager injection (drvctl-053): the roster + the coordinator protocol, keyed
	// off `wants:`. This is where the manager is told it is a manager; the spec
	// above it reads as a charter, not a work item.
	if len(edges.Wants) > 0 {
		b.WriteString(teamSection(root, edges.Wants))
		b.WriteString(coordinatorProtocol)
	}

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

	// The wake-reason tail (drvctl-053): the spine-authored `woken-by` activation
	// event (drvctl-051), rendered as the *freshest* line the manager reads — why it
	// was woken for *this* wake. Only coordinators carry `woken-by` events, so a
	// worker brief ends at the recall hint above.
	if tail := wakeTail(a.Events); tail != "" {
		b.WriteString(tail)
	}
	return b.String(), nil
}

// teamSection renders `## Your team`: one bullet per `wants:` child showing the
// control state of its latest attempt (the existing project.Control fold, pointed
// at the child — runtime Desired/Live are unknown in the log tier, so it folds to
// the log-derived State) and that attempt's open escalations. A wanted ticket with
// no attempt yet is named but carries no state; the daemon mints one when the
// coordinator pulls it in.
func teamSection(root store.Root, wants []string) string {
	var b strings.Builder
	b.WriteString("## Your team\n\n")
	b.WriteString("You are a **manager**: the tickets below are your `wants:` children. " +
		"Your job is to coordinate them, not to implement them. Pull a child's detail " +
		"with `draiver recall <child> <seq>` / `draiver brief <child>`.\n\n")
	for _, child := range wants {
		ids, err := root.ListAttempts(child)
		if err != nil || len(ids) == 0 {
			fmt.Fprintf(&b, "- `%s` — no attempt yet\n", child)
			continue
		}
		latest := ids[len(ids)-1]
		ca, err := project.LoadAttempt(root, child, latest)
		if err != nil {
			fmt.Fprintf(&b, "- `%s`/%s — (could not load: %v)\n", child, latest, err)
			continue
		}
		fmt.Fprintf(&b, "- `%s`/%s — **%s**", child, latest, ca.Control())
		if len(ca.OpenEscalations) == 0 {
			b.WriteString("\n")
			continue
		}
		b.WriteString(", open escalations:\n")
		for _, esc := range ca.OpenEscalations {
			fmt.Fprintf(&b, "  - #%d: %s\n", esc.Seq, kb.Headline(esc.Body))
		}
	}
	b.WriteString("\n")
	return b.String()
}

// coordinatorProtocol is the static how-to-coordinate block injected into every
// manager brief (docs/manager-as-worker.md §"The manager must be told it is a
// manager"). It is the manager's analog of a worker's acceptance criteria: its
// obligation on every wake is to make the critical path progress or to escalate
// once because the human is genuinely required.
const coordinatorProtocol = `## Coordinator protocol

Your spec above is a **charter**, not a work item — do not implement it yourself.
On this wake:

1. **Assess** the team: which children are blocked, which reached Review/Done, what
   the critical path needs next. Pull detail with ` + "`recall`/`brief`" + ` on a child.
2. **Act with your executive power** on what you can unblock yourself —
   ` + "`draiver resolve --by <you> <child> <seq> \"…\"`" + ` to answer a child's escalation,
   ` + "`draiver log --by <you> <child> …`" + ` to write direction/context into a child's log.
   (Edge-authorised by ` + "`wants:`" + `; you may direct and unblock, never ratify a child's
   review or mark it done.)
3. **Escalate once, consolidated**, only when a human is genuinely needed — one
   integration-level ask on *your* ticket (` + "`draiver escalate`" + `), not one per child.
   You are the single desk item; the human answers you and you relay it down.
4. Otherwise **` + "`draiver park`" + `** — stand down to Pending. You may park iff the
   critical path is progressing and nothing in your queue needs you; the spine wakes
   you again on the next child doorbell or on the human's answer.

`

// wakeTail renders the manager's wake-reason tail from the latest `woken-by`
// activation event on its own log (drvctl-051). It returns the empty string when
// there is none — a coordinator woken for the first time, or any worker — so the
// brief simply ends at the recall hint. The reconciler-authored body already reads
// as a human sentence naming the child and doorbell seq; the leading arrow marks it
// as the freshest line.
func wakeTail(events []event.Event) string {
	var latest *event.Event
	for i := range events {
		if events[i].Type == wokenByType {
			latest = &events[i]
		}
	}
	if latest == nil {
		return ""
	}
	body := strings.TrimSpace(latest.Body)
	if body == "" {
		// Fall back to the structured payload if the body is somehow empty.
		body = fmt.Sprintf("Woken by `%s` #%d.", latest.WokenChild, latest.WokenSeq)
	}
	return fmt.Sprintf("\n⟵ **You were woken for this wake:** %s\n", body)
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
