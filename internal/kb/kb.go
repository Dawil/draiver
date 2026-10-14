// Package kb is draiver's deterministic *knowledge* projection over an attempt's
// immutable, hash-chained log — the progressive-reveal fold drv-026 introduces.
// Where internal/project folds the log into the human's *control* state (Running /
// Needs me / …), kb folds the same events into the set of currently-*live* facts an
// agent cold-starts from: a bounded index of headlines it can pull full bodies and
// supersession history from on demand.
//
// The fold is pure over the log and recognises exactly ONE truth primitive beyond
// the raw event stream — the `supersedes` edge. It reads that edge from two sources
// and no others:
//
//  1. an event's explicit event.Supersedes field (`log --supersedes <seq>`), and
//  2. the one pre-existing resolution→escalation relationship it *subsumes*: a
//     `resolution` event retires the escalation(s) in its Refs ("an open escalation
//     is a type=escalation event that nothing supersedes").
//
// Reading the resolution bridge here — rather than changing the Resolve gateway to
// stamp Supersedes — means old (Refs-only) and new resolutions fold identically, so
// an attempt created before drv-026 projects correctly with zero migration. The
// fold assigns NO meaning to any other event type and none at all to tag values:
// tags are an opaque, agent-owned retrieval axis, so tag drift degrades search
// only, never truth.
package kb

import (
	"sort"
	"strings"

	"github.com/Dawil/draiver/internal/event"
)

// maxHeadline caps a headline's length (in runes) so the cold-start index stays
// bounded regardless of body size. A longer first line is truncated with an ellipsis.
const maxHeadline = 100

// Fact is one event annotated by the fold: its liveness, its derived headline, and —
// when retired — the seq of the event that superseded it.
type Fact struct {
	event.Event
	// Live is true iff no later event supersedes this one.
	Live bool
	// Headline is the first non-empty body line, stripped of leading markup and
	// length-capped — the searchable key the cold-start index pushes.
	Headline string
	// SupersededBy is the seq of the earliest later event that retired this fact, or
	// 0 when the fact is live. The inverse of the supersedes edge, for display.
	SupersededBy int
}

// View is the folded knowledge projection of one attempt's log. Facts holds every
// event in seq order, each annotated; the maps index the supersession edge both ways.
type View struct {
	Facts []Fact
	bySeq map[int]*Fact
	// retires[seq] lists the older seqs this event supersedes (explicit + bridged).
	retires map[int][]int
}

// retiredBy returns the seqs an event supersedes: its explicit Supersedes field plus,
// for a resolution, the escalation(s) in its Refs (the subsumed special case).
func retiredBy(e event.Event) []int {
	out := make([]int, 0, len(e.Supersedes)+len(e.Refs))
	out = append(out, e.Supersedes...)
	if e.Type == "resolution" {
		out = append(out, e.Refs...)
	}
	return out
}

// Fold projects events (which must be in seq order) into the knowledge View. It is
// pure: no I/O, no clock, no mutation of the input.
func Fold(events []event.Event) View {
	// First pass: who retires whom, and the earliest event that retires each seq.
	retires := map[int][]int{}
	supersededBy := map[int]int{}
	for _, e := range events {
		for _, target := range retiredBy(e) {
			retires[e.Seq] = append(retires[e.Seq], target)
			// Events arrive in seq order, so the first writer wins → the earliest
			// superseding event is recorded.
			if _, seen := supersededBy[target]; !seen {
				supersededBy[target] = e.Seq
			}
		}
	}

	v := View{bySeq: map[int]*Fact{}, retires: retires}
	v.Facts = make([]Fact, len(events))
	for i, e := range events {
		by := supersededBy[e.Seq]
		v.Facts[i] = Fact{
			Event:        e,
			Live:         by == 0,
			Headline:     Headline(e.Body),
			SupersededBy: by,
		}
	}
	for i := range v.Facts {
		v.bySeq[v.Facts[i].Seq] = &v.Facts[i]
	}
	return v
}

// Live returns the live facts in seq order — the cold-start index.
func (v View) Live() []Fact {
	var out []Fact
	for _, f := range v.Facts {
		if f.Live {
			out = append(out, f)
		}
	}
	return out
}

// LiveEscalations returns the live `escalation` facts in seq order — the open
// escalations, derived from the same supersedes fold as everything else (a resolved
// escalation is retired by the resolution bridge, so it is not live).
func (v View) LiveEscalations() []Fact {
	var out []Fact
	for _, f := range v.Live() {
		if f.Type == "escalation" {
			out = append(out, f)
		}
	}
	return out
}

// Get returns the fact for a seq, if present.
func (v View) Get(seq int) (Fact, bool) {
	f, ok := v.bySeq[seq]
	if !ok {
		return Fact{}, false
	}
	return *f, true
}

// Chain returns the full supersession history of a fact — seq plus the transitive
// closure of events reachable via the supersedes edge (the "seemed-like-X → now-Y"
// history) — oldest-to-newest. An unknown seq yields an empty slice; a fact that
// supersedes nothing yields just itself.
func (v View) Chain(seq int) []Fact {
	if _, ok := v.bySeq[seq]; !ok {
		return nil
	}
	seen := map[int]bool{}
	var walk func(int)
	walk = func(s int) {
		if seen[s] {
			return
		}
		seen[s] = true
		for _, older := range v.retires[s] {
			walk(older)
		}
	}
	walk(seq)
	var out []Fact
	for s := range seen {
		if f, ok := v.bySeq[s]; ok {
			out = append(out, *f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Headline derives a fact's searchable key: the first non-empty line of the body,
// stripped of leading Markdown heading/quote/list markup and capped at maxHeadline
// runes (deterministic; an empty body yields "(no body)").
func Headline(body string) string {
	for _, ln := range strings.Split(body, "\n") {
		ln = strings.TrimSpace(ln)
		// Strip leading heading (#), blockquote (>), and list (-, *, +) markers.
		ln = strings.TrimLeft(ln, "#>-*+ \t")
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		return truncateRunes(ln, maxHeadline)
	}
	return "(no body)"
}

// truncateRunes caps s at n runes, appending an ellipsis when it had to cut.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}
