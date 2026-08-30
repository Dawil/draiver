package project

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Dawil/draiver/internal/store"
)

// Edges are a ticket's authored dependency edges, carried in its spec.md
// frontmatter — the first *relational* fields draiver's unit file holds. Each
// list is a set of ticket ids; the semantics live in
// docs/capabilities-and-supervision.md (§The three relations):
//
//   - Wants:    tickets this one pulls in — enable flows down, escalation up.
//   - After:    tickets this one is admitted after they reach Review.
//   - Requires: tickets this one is admitted after they reach Done.
//
// This ticket (drvctl-037) is the data model only: parse, author, reverse-index,
// and cycle-refuse. No reconciler consumes these yet.
type Edges struct {
	Wants    []string `yaml:"wants"`
	After    []string `yaml:"after"`
	Requires []string `yaml:"requires"`
}

// All returns the union of the three relations as a single directed adjacency
// list (this ticket → the tickets it points at). Cycle safety treats the three
// as one DAG: an edge of any kind that closes a loop is refused, so the
// distinction between relation types does not matter to the reachability check.
func (e Edges) All() []string {
	var out []string
	out = append(out, e.Wants...)
	out = append(out, e.After...)
	out = append(out, e.Requires...)
	return out
}

// LoadEdges reads a ticket's authored dependency edges from its spec.md. A
// ticket whose spec has no frontmatter (or none of the three keys) yields a
// zero Edges — omitted fields are fine, and unknown frontmatter keys are ignored
// by the struct unmarshal, so this never fails on an unrelated field.
func LoadEdges(root store.Root, ticket string) (Edges, error) {
	block, ok, err := specFrontmatter(root.SpecPath(ticket))
	if err != nil || !ok {
		return Edges{}, err
	}
	var e Edges
	if err := yaml.Unmarshal([]byte(block), &e); err != nil {
		return Edges{}, fmt.Errorf("parse spec edges for %s: %w", ticket, err)
	}
	return e, nil
}

// LoadAllEdges reads the authored edges of every ticket under the root, keyed by
// ticket id. It is the substrate for both the reverse index and the author-time
// cycle check.
func LoadAllEdges(root store.Root) (map[string]Edges, error) {
	tickets, err := root.ListTickets()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Edges, len(tickets))
	for _, t := range tickets {
		e, err := LoadEdges(root, t)
		if err != nil {
			return nil, err
		}
		out[t] = e
	}
	return out, nil
}

// ReverseWants builds the reverse index over `wants:` — for each ticket Y, the
// tickets X whose spec.md `wants:` names Y (i.e. "who wants Y"). This is the
// model-layer substrate for reverse-wants activation (drvctl-041): a child's
// escalation must wake every ticket that wants it. Wanters are sorted and
// de-duplicated so the result is deterministic regardless of scan order.
func ReverseWants(root store.Root) (map[string][]string, error) {
	all, err := LoadAllEdges(root)
	if err != nil {
		return nil, err
	}
	return reverseWants(all), nil
}

func reverseWants(all map[string]Edges) map[string][]string {
	rev := map[string]map[string]bool{}
	for x, e := range all {
		for _, y := range e.Wants {
			if rev[y] == nil {
				rev[y] = map[string]bool{}
			}
			rev[y][x] = true
		}
	}
	out := make(map[string][]string, len(rev))
	for y, wanters := range rev {
		list := make([]string, 0, len(wanters))
		for x := range wanters {
			list = append(list, x)
		}
		sort.Strings(list)
		out[y] = list
	}
	return out
}

// WantsClosure returns the set of tickets transitively reachable down `wants:`
// edges from any source ticket, excluding the sources themselves. Sources are
// seeded into the frontier so their edges are traversed (reaching grand-children)
// even though they are already desired; only *non*-source reachable tickets are
// returned as wanted. The `seen` guard tolerates a hand-edited cyclic spec — the
// authoring seam refuses cycles (drvctl-037) but this must not spin regardless.
//
// It is the shared graph walk behind both reconcile's admission propagation
// (drvctl-038) and the project-tier desired/Pending projection (DeriveDesired,
// drvctl-039), so the two agree on who a set of enabled parents pulls in.
func WantsClosure(sources map[string]bool, edges map[string]Edges) map[string]bool {
	seen := make(map[string]bool, len(sources))
	frontier := make([]string, 0, len(sources))
	for t := range sources {
		seen[t] = true
		frontier = append(frontier, t)
	}
	wanted := map[string]bool{}
	for len(frontier) > 0 {
		t := frontier[0]
		frontier = frontier[1:]
		for _, child := range edges[t].Wants {
			if seen[child] {
				continue
			}
			seen[child] = true
			frontier = append(frontier, child)
			if !sources[child] {
				wanted[child] = true
			}
		}
	}
	return wanted
}

// Ref identifies one attempt (ticket + attempt id) — a lightweight key for the
// desired set, so the project tier need not import worktree.Key.
type Ref struct {
	Ticket  string
	Attempt string
}

// DeriveDesired computes the declaratively-desired attempt set — the supervised
// fleet the daemon would keep a live session for — from the durable log + spec
// `wants:` edges alone. It is the read-side twin of reconcile.desired's declarative
// half (drvctl-038): an attempt is desired if it is directly enabled and Running,
// or it is the latest-Running attempt of a ticket transitively `wants:`-reachable
// from such a directly-enabled ticket. This is what the Pending projection means by
// "desired", so an enabled-via-parent child that has not been admitted yet still
// derives Pending.
//
// It deliberately omits the two daemon-only pieces of reconcile.desired: the
// transient imperative `ctl start` markers (they need a live controller nonce, so a
// read-only `status` cannot honour them) and minting an absent child (a write). A
// wanted child with no attempt yet therefore contributes nothing here — there is no
// attempt to project a state onto until the daemon mints one.
//
// Pure given `all` (as LoadAll returns it: sorted by ticket then attempt id, so a
// ticket's last entry is its latest attempt) and the edge map.
func DeriveDesired(all []Attempt, edges map[string]Edges) map[Ref]bool {
	desired := map[Ref]bool{}
	byTicket := map[string][]Attempt{}
	sources := map[string]bool{}
	for _, a := range all {
		byTicket[a.Ticket] = append(byTicket[a.Ticket], a)
		if a.Enabled && a.State == Running {
			desired[Ref{a.Ticket, a.ID}] = true
			sources[a.Ticket] = true
		}
	}
	for child := range WantsClosure(sources, edges) {
		atts := byTicket[child]
		if len(atts) == 0 {
			continue // no attempt yet; the daemon would mint one — nothing to project
		}
		latest := atts[len(atts)-1]
		if latest.State == Running {
			desired[Ref{latest.Ticket, latest.ID}] = true
		}
	}
	return desired
}

// reachedThreshold captures how far a ticket's work has progressed for the
// forward gate's two admission thresholds: `review` is set once any attempt is at
// Review-or-Done, `done` once any attempt is Done. It is the read-side twin of
// reconcile.reachedState — the project tier cannot import the daemon's reconcile
// package (that would be a cycle), so, exactly as DeriveDesired twins
// reconcile.desired, the threshold read is mirrored here for the board's
// "waiting on X" reason.
type reachedThreshold struct {
	review bool // an attempt is at Review or Done
	done   bool // an attempt is at Done
}

// bestReached folds every attempt's derived control state into a per-ticket
// threshold, taking the best (most-succeeded) state across a ticket's attempts —
// success is monotonic, so a later re-run never retracts a threshold an earlier
// attempt crossed. Mirrors reconcile.bestReached.
func bestReached(all []Attempt) map[string]reachedThreshold {
	out := map[string]reachedThreshold{}
	for _, a := range all {
		rs := out[a.Ticket]
		switch a.State {
		case Done:
			rs.done = true
			rs.review = true // Done is past Review, so it satisfies `after:` too
		case Review:
			rs.review = true
		}
		out[a.Ticket] = rs
	}
	return out
}

// WaitingReason is the human-facing line a board card shows for a Pending
// attempt: the "waiting on X" peek the design (§The Pending state) carves out as
// the one part of the Pending projection that reads a *sibling* ticket's state.
// It names the ordering predecessors still holding the forward gate (drvctl-040)
// shut — each `after:` predecessor not yet at Review, each `requires:` predecessor
// not yet at Done — reusing the same thresholds as a read-side twin, so the reason
// names exactly what the gate is waiting on. Predecessors are reported in
// authored order (after: before requires:), de-duplicated when a ticket appears in
// both.
//
// With every predecessor satisfied — or none authored — the attempt is Pending
// for want of admission (desired but no live agent yet: queued for the supervisor,
// or waiting on a wants:-parent activation), not an edge, so the reason is the
// generic "waiting to start". `all` is LoadAll's output; `edges` is LoadAllEdges'.
func WaitingReason(ticket string, all []Attempt, edges map[string]Edges) string {
	e := edges[ticket]
	reached := bestReached(all)
	var unmet []string
	seen := map[string]bool{}
	add := func(x string) {
		if !seen[x] {
			seen[x] = true
			unmet = append(unmet, x)
		}
	}
	for _, x := range e.After {
		if !reached[x].review {
			add(x)
		}
	}
	for _, x := range e.Requires {
		if !reached[x].done {
			add(x)
		}
	}
	if len(unmet) == 0 {
		return "waiting to start"
	}
	return "waiting on " + strings.Join(unmet, ", ")
}

// CheckCycle reports the first dependency cycle reachable in the union graph, as
// the offending path (e.g. ["A", "B", "C", "A"]), or nil if the edges form a
// DAG. A self-edge surfaces here too, as the length-2 path ["A", "A"]. The three
// relations are unioned: a cycle spanning, say, a `wants:` and an `after:` edge
// is still a cycle.
func CheckCycle(all map[string]Edges) []string {
	// Graph over every id that appears as a source or a target — a target with no
	// spec of its own contributes no outgoing edges but must still be a visitable
	// node so a cycle passing through it is found.
	const (
		white = 0 // unvisited
		gray  = 1 // on the current DFS stack
		black = 2 // fully explored, no cycle through it
	)
	color := map[string]int{}
	var stack []string

	var dfs func(node string) []string
	dfs = func(node string) []string {
		color[node] = gray
		stack = append(stack, node)
		for _, next := range all[node].All() {
			switch color[next] {
			case gray:
				// Back-edge: the cycle is the stack tail from `next` onward, closed
				// by `next` again.
				for i, n := range stack {
					if n == next {
						return append(append([]string{}, stack[i:]...), next)
					}
				}
			case white:
				if path := dfs(next); path != nil {
					return path
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[node] = black
		return nil
	}

	// Deterministic node order so the reported path is stable across runs.
	nodes := map[string]bool{}
	for src, e := range all {
		nodes[src] = true
		for _, dst := range e.All() {
			nodes[dst] = true
		}
	}
	ordered := make([]string, 0, len(nodes))
	for n := range nodes {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)
	for _, n := range ordered {
		if color[n] == white {
			if path := dfs(n); path != nil {
				return path
			}
		}
	}
	return nil
}

// FormatCyclePath renders a cycle path for a human-facing refusal message.
func FormatCyclePath(path []string) string {
	return strings.Join(path, " → ")
}
