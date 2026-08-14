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
