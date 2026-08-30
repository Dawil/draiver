// Package repo is draiver's in-process data-access gateway: the one mid-level
// library that composes store/ticketlog/project/event/attempt into the typed
// operations both the CLI verbs (cmd/) and the webui (internal/web) perform on the
// data folder. It sits ABOVE the pure primitives (store.Root path helpers,
// ticketlog.Read/Append, project.Derive*/LoadAttempt, event.ValidateLink) and
// reuses them — it does not replace them.
//
// A Repo holds a resolved data root and the acting identity, folding actor
// stamping, append-time link validation, and each verb's pre-append checks into a
// single implementation of every write. So a board-authored and a terminal-
// authored event are byte-identical, and there is exactly one code path per write —
// a function, not a subprocess. Actor resolution is a constructor param
// (human:$USER for the CLI, human:webui/configured for the server), not a package
// global.
//
// Scope is the ctl-verb + webui authoring gateway. The supervisor daemon and
// agent-session subsystems (internal/reconcile, gate, watch, limit, completion)
// emit their own operational events with the daemon's own actor; those sit beside
// this package over the same primitives, not behind it.
package repo

import (
	"fmt"

	"github.com/Dawil/draiver/internal/config"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// Repo is the resolved (root, actor) handle every operation folds through.
type Repo struct {
	root  store.Root
	actor string
}

// New builds a gateway bound to a data root and the acting identity. The actor is
// stamped on every authored event whose own Actor is unset — human:$USER for the
// CLI, human:webui or a configured identity for the server.
func New(root store.Root, actor string) *Repo {
	return &Repo{root: root, actor: actor}
}

// Root is the bound data root, for callers that still need the pure path layer.
func (r *Repo) Root() store.Root { return r.root }

// Actor is the identity stamped on authored events.
func (r *Repo) Actor() string { return r.actor }

// AppendAt appends e to an already-resolved (ticket, attempt): it checks the
// attempt exists, validates any review links against the append-time safety floor
// (the hardcoded {http,https} scheme allowlist plus the optional per-deployment
// host allowlist) before the single write, stamps the actor when e.Actor is unset,
// and appends. It is the shared core every typed write folds through — the
// in-process replacement for cmd's appendEventAt. Config is loaded only when links
// are present, so a link-less append pays nothing.
func (r *Repo) AppendAt(ticket, att string, e event.Event) (event.Event, error) {
	if !r.root.AttemptExists(ticket, att) {
		return event.Event{}, fmt.Errorf("attempt %s/%s not found", ticket, att)
	}
	if len(e.Links) > 0 {
		cfg, err := config.Load("")
		if err != nil {
			return event.Event{}, err
		}
		for _, l := range e.Links {
			if err := event.ValidateLink(l, cfg.ReviewLinkHosts); err != nil {
				return event.Event{}, fmt.Errorf("invalid --link %s=%s: %w", l.Rel, l.Href, err)
			}
		}
	}
	if e.Actor == "" {
		e.Actor = r.actor
	}
	return ticketlog.Append(r.root, ticket, att, e)
}

// AppendTyped is the generic recorder behind `draiver log`: it appends a typed
// event (note/gotcha/decision/…, with optional refs/artefacts/links) to an
// already-resolved (ticket, attempt) through the shared write core.
func (r *Repo) AppendTyped(ticket, att string, e event.Event) (event.Event, error) {
	return r.AppendAt(ticket, att, e)
}

// Done records the terminal `done` event closing an attempt.
func (r *Repo) Done(ticket, att, body string) (event.Event, error) {
	return r.AppendAt(ticket, att, event.Event{Type: "done", Body: body})
}

// Enable opts an attempt into daemon supervision (the durable `enable` event `ctl
// up` honours). Disable parks it.
func (r *Repo) Enable(ticket, att string) (event.Event, error) {
	return r.setEnabled(ticket, att, true)
}

// Disable records that an attempt should leave the supervised fleet.
func (r *Repo) Disable(ticket, att string) (event.Event, error) {
	return r.setEnabled(ticket, att, false)
}

// setEnabled appends the enable/disable lifecycle event, with the same body copy
// the CLI verb writes so a board- and a terminal-authored toggle are identical.
func (r *Repo) setEnabled(ticket, att string, enable bool) (event.Event, error) {
	typ, verb := "disable", "disabled"
	if enable {
		typ, verb = "enable", "enabled"
	}
	return r.AppendAt(ticket, att, event.Event{
		Type: typ,
		Body: fmt.Sprintf("Supervision %s for %s/%s.", verb, ticket, att),
	})
}

// Supervision records a Capability's coordinator supervision mode as a
// `supervision` control event (the mode rides on Outcome, like archive's
// sentiment). The caller validates the mode before calling.
func (r *Repo) Supervision(ticket, att string, mode project.Supervision) (event.Event, error) {
	return r.AppendAt(ticket, att, event.Event{
		Type:    "supervision",
		Outcome: string(mode),
		Body:    fmt.Sprintf("Coordinator supervision set to %s for %s/%s.", mode, ticket, att),
	})
}

// Resolve answers an open escalation: it re-checks that seq names an escalation on
// this attempt (the CLI's own pre-append validation), then appends the resolution
// referencing it. Escalation and resolution together form one durable artefact.
func (r *Repo) Resolve(ticket, att string, seq int, answer string) (event.Event, error) {
	events, err := ticketlog.Read(r.root, ticket, att)
	if err != nil {
		return event.Event{}, err
	}
	var found *event.Event
	for i := range events {
		if events[i].Seq == seq {
			found = &events[i]
			break
		}
	}
	if found == nil {
		return event.Event{}, fmt.Errorf("no event #%d on %s/%s", seq, ticket, att)
	}
	if found.Type != "escalation" {
		return event.Event{}, fmt.Errorf("event #%d on %s/%s is a %q, not an escalation", seq, ticket, att, found.Type)
	}
	return r.AppendAt(ticket, att, event.Event{
		Type: "resolution",
		Refs: []int{seq},
		Body: answer,
	})
}

// ArchiveResult reports what an Archive/Unarchive did: the written event and, for
// archive, the resolved sentiment; NoOp is true when the attempt was already in
// the target board-membership state and nothing was written.
type ArchiveResult struct {
	Event   event.Event
	Outcome string
	NoOp    bool
}

// Archive takes an attempt off the board (an `archive` event, last-wins under
// DeriveArchived) without touching its lifecycle State. outcome is the sentiment:
// pass "accepted"/"abandoned" to force it, or "" to derive it from State
// (Done→accepted, else abandoned) exactly as the board does. Archiving an
// already-archived attempt is a reported no-op — the single implementation of
// "what an archive is", shared by the CLI verb and the board's tick/cross.
func (r *Repo) Archive(ticket, att, outcome string) (ArchiveResult, error) {
	a, err := project.LoadAttempt(r.root, ticket, att)
	if err != nil {
		return ArchiveResult{}, err
	}
	if a.Archived {
		return ArchiveResult{NoOp: true}, nil
	}
	if outcome == "" {
		outcome = ArchiveOutcome(a.State)
	}
	e, err := r.AppendAt(ticket, att, event.Event{
		Type:    "archive",
		Outcome: outcome,
		Body:    ArchiveBody(outcome),
	})
	if err != nil {
		return ArchiveResult{}, err
	}
	return ArchiveResult{Event: e, Outcome: outcome}, nil
}

// Unarchive returns an archived attempt to the board. It carries no sentiment.
// Unarchiving an attempt already on the board is a reported no-op.
func (r *Repo) Unarchive(ticket, att string) (ArchiveResult, error) {
	a, err := project.LoadAttempt(r.root, ticket, att)
	if err != nil {
		return ArchiveResult{}, err
	}
	if !a.Archived {
		return ArchiveResult{NoOp: true}, nil
	}
	e, err := r.AppendAt(ticket, att, event.Event{
		Type: "unarchive",
		Body: "Unarchived and returned to the board.",
	})
	if err != nil {
		return ArchiveResult{}, err
	}
	return ArchiveResult{Event: e}, nil
}

// ArchiveOutcome derives the archive sentiment from lifecycle State, mirroring the
// board's archiveAction: a finished (Done) attempt is accepted, any active one
// (Running/Pending/Needs me/Review) is abandoned. It maps to the exact Outcome
// strings the board and the CLI write so their archives are indistinguishable in
// the log and to metrics.
func ArchiveOutcome(s project.State) string {
	if s == project.Done {
		return "accepted"
	}
	return "abandoned"
}

// ArchiveBody is the human-readable body stamped on an archive event, keyed off
// the sentiment. It is the single copy both the CLI verb and the webui write; the
// machine-readable truth is the event's Outcome field.
func ArchiveBody(outcome string) string {
	if outcome == "accepted" {
		return "Accepted and archived from the board."
	}
	return "Closed and archived from the board."
}

// LoadAttempt loads one attempt's derived state and event log.
func (r *Repo) LoadAttempt(ticket, att string) (project.Attempt, error) {
	return project.LoadAttempt(r.root, ticket, att)
}

// ReadLog returns an attempt's raw event log.
func (r *Repo) ReadLog(ticket, att string) ([]event.Event, error) {
	return ticketlog.Read(r.root, ticket, att)
}

// LoadBoard loads every attempt and the fleet-wide dependency edges — the raw
// material the webui shapes into board columns and the CLI's status view prints.
// The board/derive shaping stays in the caller; the load lives here.
func (r *Repo) LoadBoard() ([]project.Attempt, map[string]project.Edges, error) {
	attempts, err := project.LoadAll(r.root)
	if err != nil {
		return nil, nil, err
	}
	edges, err := project.LoadAllEdges(r.root)
	if err != nil {
		return nil, nil, err
	}
	return attempts, edges, nil
}
