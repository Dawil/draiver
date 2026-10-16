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
	"github.com/Dawil/draiver/internal/ticketlog"
)

// Kind labels what can wake a parked attempt. It is an opaque discriminator the
// board/brief may render; the park guard only cares whether any source exists.
type Kind string

const (
	// Wants is the coordinator wake source: a `wants:` ticket whose children's
	// lifecycle (a child escalation under pre-digest, reverse_wants.go) re-admits
	// it. Pending while any wanted child is not yet Done.
	Wants Kind = "wants"

	// AsyncJob is the worker wake source drvctl-055 adds: a detached `draiver test`
	// (or any tracked async job) fired off on the attempt's OWN log, whose result
	// will land back on that log and path-activate the parked worker. Pending from
	// the moment the job is launched until its result event lands. This is what lets
	// `park` generalise past the manager — a plain leaf worker can `launch-async &&
	// park` behind the same seam, with no `wants:` edge.
	AsyncJob Kind = "async-job"
)

// asyncJobType is the log event the launcher appends to register an async-job wake
// source — the detached `draiver test` launch marker (drvctl-055). It is not a
// lifecycle event (it never moves control state) and is "answered" by the first
// `test-result` that lands after it. Kept in sync with cmd's and reconcile's copies
// of the same string (there is no central event-type registry; the literals are
// local by the package convention the rest of the log types follow).
const asyncJobType = "async-job"

// testResultType is the result event a detached runner lands when the climb
// finishes (pass or fail) — the doorbell that answers an async-job launch and wakes
// the parked worker.
const testResultType = "test-result"

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
	var sources []Source

	// The worker async-job source (drvctl-055): a detached job in flight on this
	// attempt's own log, independent of any `wants:` edge, so a leaf worker is
	// parkable while its test runs.
	aj, err := pendingAsyncJob(root, ticket, att)
	if err != nil {
		return nil, err
	}
	if aj != nil {
		sources = append(sources, *aj)
	}

	// The coordinator wants source (drvctl-052): a `wants:` ticket with at least one
	// child not yet Done, whose lifecycle can still fire reverse-`wants:` activation.
	w, err := pendingWants(root, ticket)
	if err != nil {
		return nil, err
	}
	if w != nil {
		sources = append(sources, *w)
	}

	return sources, nil
}

// pendingWants returns the coordinator wake source (drvctl-052) when ticket
// `wants:` at least one child not yet Done, or nil for a leaf / an all-children-Done
// coordinator (which reviews rather than parks).
func pendingWants(root store.Root, ticket string) (*Source, error) {
	edges, err := project.LoadEdges(root, ticket)
	if err != nil {
		return nil, err
	}
	if len(edges.Wants) == 0 {
		return nil, nil // a leaf, not a coordinator — no wants source.
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
	return &Source{
		Kind: Wants,
		Desc: fmt.Sprintf("wanted children still in flight: %v", inflight),
	}, nil
}

// pendingAsyncJob returns the async-job wake source (drvctl-055) when the attempt's
// own log carries a launched job that has not yet landed its result — the window in
// which a detached `draiver test` is running and will path-activate the worker when
// it finishes. A launch is "answered" by any `test-result` that lands after it, so a
// completed (or never-launched) attempt yields no source and park falls through to
// review/escalate. Serial by construction: the pattern is launch-one, park, wake on
// the result, so the latest launch is the one that matters.
func pendingAsyncJob(root store.Root, ticket, att string) (*Source, error) {
	events, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		return nil, err
	}
	lastLaunch := 0
	for _, e := range events {
		if e.Type == asyncJobType && e.Seq > lastLaunch {
			lastLaunch = e.Seq
		}
	}
	if lastLaunch == 0 {
		return nil, nil // no async job ever launched on this log.
	}
	for _, e := range events {
		if e.Type == testResultType && e.Seq > lastLaunch {
			return nil, nil // the launch is already answered by a result.
		}
	}
	return &Source{
		Kind: AsyncJob,
		Desc: "a detached test is running; its result will land on this log and wake the attempt",
	}, nil
}
