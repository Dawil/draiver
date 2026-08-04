// Package gate is the supervisor's "Gate" step (reconcile loop step 3) and the
// second of the two hooks: the permission gate. It routes an agent's
// tool-permission callback to the human-in-the-loop escalation seam instead of
// auto-approving it — the agent's own authority boundary becomes the gate.
//
// When a session surfaces an agent.EventPermission ("may I use this tool?"), a
// Gate consults a layered Policy:
//
//   - Allow  → answer the agent so the call proceeds. The session keeps working.
//   - Escalate → append an `escalation` event to the attempt's durable,
//     hash-chained log, deny the pending call, and halt the session. The open
//     escalation is what moves the attempt to Needs-me (project.Derive maps an
//     unresolved escalation → NeedsMe); the session id + log survive for a
//     `--resume` once a human resolves it.
//
// A Gate drives one attempt's session and is not safe for concurrent use: call
// Consider from the single goroutine that ranges the adapter's Stream, alongside
// the watch.Watcher.
package gate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// Decider answers a tool-permission request back to the agent. The claudecode
// adapter satisfies it via agent.Permissioner; the Gate holds the interface so a
// test can substitute a fake without a live session.
type Decider interface {
	Decide(ctx context.Context, requestID string, d agent.Decision) error
}

// Gate applies a permission Policy to one attempt's session. Construct it with
// New. It appends escalations into ticket/attempt's log (attributing them to
// actor), answers the agent through dec, and reaps the session through halt.
type Gate struct {
	root    store.Root
	ticket  string
	attempt string
	actor   string
	policy  Policy
	dec     Decider
	halt    func() error
}

// New returns a Gate that enforces policy on ticket/attempt's session. dec
// answers permission requests back to the agent (typically the adapter, which
// implements agent.Permissioner). halt reaps the session on an escalation
// (typically manage.Handle.Kill); it may be nil, in which case an escalation is
// still recorded and the call denied, but the caller is left to stop the
// session. dec must be non-nil.
func New(root store.Root, ticket, attempt, actor string, policy Policy, dec Decider, halt func() error) *Gate {
	return &Gate{
		root:    root,
		ticket:  ticket,
		attempt: attempt,
		actor:   actor,
		policy:  policy,
		dec:     dec,
		halt:    halt,
	}
}

// Outcome is what Consider did with an event.
type Outcome int

const (
	// Ignored: the event was not a permission request; the Gate did nothing.
	Ignored Outcome = iota
	// Allowed: a permission request the policy auto-approved; the agent was told
	// to proceed.
	Allowed
	// Escalated: a permission request the policy gated; an escalation was
	// recorded, the call denied, and (if halt was set) the session reaped.
	Escalated
)

// Consider handles one normalized event. Non-permission events are a no-op
// (Ignored). For an agent.EventPermission it applies the policy: an allowed tool
// is answered so the session proceeds (Allowed); a gated tool is turned into a
// durable escalation that halts the session (Escalated). The escalation is
// written before the session is halted, so the durable record survives even if
// the reap fails.
func (g *Gate) Consider(ctx context.Context, ev agent.Event) (Outcome, error) {
	if ev.Kind != agent.EventPermission || ev.Permission == nil {
		return Ignored, nil
	}
	req := ev.Permission

	if g.policy.Decide(req.Tool) == Allow {
		if err := g.dec.Decide(ctx, req.ID, agent.Decision{Allow: true, Input: req.Input}); err != nil {
			return Ignored, fmt.Errorf("gate: approve %s: %w", req.Tool, err)
		}
		return Allowed, nil
	}

	// Gated: the escalation is the load-bearing side effect — it is what flips
	// the attempt to Needs-me — so record it first and fail the whole gate if it
	// cannot be written.
	if _, err := ticketlog.Append(g.root, g.ticket, g.attempt, event.Event{
		Type:  "escalation",
		Actor: g.actor,
		Body:  escalationBody(req),
	}); err != nil {
		return Ignored, fmt.Errorf("gate: record escalation for %s: %w", req.Tool, err)
	}

	// Deny the pending call so the agent is not left waiting on an answer it will
	// never act on (the halt below reaps it regardless; best-effort).
	_ = g.dec.Decide(ctx, req.ID, agent.Decision{
		Allow:   false,
		Message: "Permission gate: this tool is escalated to a human. The session is being stopped; a resumed session continues once the escalation is resolved.",
	})

	if g.halt != nil {
		if err := g.halt(); err != nil {
			// The escalation is already durable; report the halt failure but keep
			// the Escalated outcome so the caller knows the attempt is now blocked.
			return Escalated, fmt.Errorf("gate: halt after escalation: %w", err)
		}
	}
	return Escalated, nil
}

// escalationBody renders the human-facing escalation for a gated tool call as
// Markdown (the board renders event bodies as Markdown).
func escalationBody(req *agent.PermissionRequest) string {
	body := fmt.Sprintf("Permission gate stopped the session: the agent requested **`%s`**, which the tool policy routes to a human.\n",
		req.Tool)
	if input := prettyInput(req.Input); input != "" {
		body += "\nRequested call:\n\n```json\n" + input + "\n```\n"
	}
	body += "\nResolve this escalation to approve the call, or widen the tool policy — then the attempt resumes from the log."
	return body
}

// prettyInput indents a tool call's raw input JSON for the escalation body,
// falling back to the compact form (or empty) if it cannot be re-rendered.
func prettyInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(pretty)
}
