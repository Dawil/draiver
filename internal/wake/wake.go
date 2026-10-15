// Package wake answers the one question `draiver park` must ask before it
// suspends an attempt: is there anything that can wake it again? A parked
// attempt drops to Pending with no live agent and no human-facing escalation, so
// it is only ever resumed by a *wake source* firing — a lifecycle transition the
// reconciler re-admits the attempt on. An attempt with no pending wake source
// that parks would sleep forever; park refuses it, and the completion gate
// refuses to treat its park as a hand-off (internal/completion).
//
// The wake source is deliberately **abstract** — "wake me when event E
// transitions" — not hard-wired to any one edge (docs/manager-as-worker.md,
// "`park` as a general primitive"). drvctl-052 ships the one source the manager
// lifecycle needs: a `wants:` coordinator whose children's lifecycle can still
// wake it via reverse-`wants:` activation. drvctl-055 adds the worker's own
// async-job source (a detached `draiver test` whose result lands on the
// attempt's own log) behind the same Pending seam, so park generalises past the
// manager with no change to its callers.
package wake

import (
	"fmt"

	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
)

// Kind labels what can wake a parked attempt. It is an opaque discriminator the
// board/brief may render; the park guard only cares whether any source exists.
type Kind string

const (
	// Wants is the coordinator wake source: a `wants:` ticket whose children's
	// lifecycle (a child escalation under pre-digest, reverse_wants.go) re-admits
	// it. Pending while any wanted child is not yet Done.
	Wants Kind = "wants"
)

// Source is one reason a parked attempt can be woken again. Desc is a
// human-facing summary recorded in the park event so a resuming agent (and the
// board) can see what the attempt is waiting on.
type Source struct {
	Kind Kind
	Desc string
}

// Pending returns the wake sources currently able to re-wake ticket/att. An
// empty result means nothing can wake it: park must refuse, and the completion
// gate must not accept a park as a hand-off, or the attempt sleeps forever.
//
// In drvctl-052 the sole source is the coordinator one: the attempt's ticket
// `wants:` at least one child that has not reached Done, so reverse-`wants:`
// activation can still fire on a child escalation. A ticket with no `wants:`
// edges (a leaf worker) has no source here yet — drvctl-055 adds the async-job
// source for that case. A coordinator whose every child is already Done has no
// future child lifecycle to wake on either (the coordinator gate admits it for
// its own final review instead of parking it), so it too yields no source.
func Pending(root store.Root, ticket, att string) ([]Source, error) {
	edges, err := project.LoadEdges(root, ticket)
	if err != nil {
		return nil, err
	}
	if len(edges.Wants) == 0 {
		return nil, nil // a leaf, not a coordinator — no wants source in 052.
	}

	all, err := project.LoadAll(root)
	if err != nil {
		return nil, err
	}
	done := map[string]bool{}
	for _, a := range all {
		if a.State == project.Done {
			done[a.Ticket] = true
		}
	}

	var inflight []string
	for _, child := range edges.Wants {
		if !done[child] {
			inflight = append(inflight, child)
		}
	}
	if len(inflight) == 0 {
		return nil, nil // every child Done: review, don't park.
	}
	return []Source{{
		Kind: Wants,
		Desc: fmt.Sprintf("wanted children still in flight: %v", inflight),
	}}, nil
}
