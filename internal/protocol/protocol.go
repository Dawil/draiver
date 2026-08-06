// Package protocol is the supervisor's protocol gate (reconcile loop step 3): it
// enforces the draiver protocol by process control rather than agent goodwill.
// Where the permission gate (internal/gate) routes "may I use this tool?" to a
// human, the protocol gate enforces the log discipline the onboarding skill can
// only ask for. It has two halves, both privileged seats the skill never holds:
//
//   - Cold-start brief. InjectBrief builds brief.Build for the attempt and Prompts
//     it into a freshly spawned session, so an agent is *always* briefed — the
//     brief stops being something the agent is trusted to fetch.
//   - Withhold-until-logged. Gate.Consider intercepts an agent's tool-permission
//     callback and refuses to forward a mutating tool (Edit/Write/NotebookEdit)
//     until a decision or gotcha justifying it is on disk. The rationale reaches
//     the durable, hash-chained log before the edit it explains does.
//
// The gate is a veto, not an approver: it only ever *denies* (withholds). When a
// tool is Free, or a justified tool's rationale is already on disk, Consider
// returns Cleared without answering the agent — allowing (or escalating) stays
// the permission gate's job, so the two compose as protocol -> permission with no
// double-answer. A withhold is transient, not an escalation: the session stays
// live so the agent can log its rationale and retry the same edit; nothing is
// halted and no escalation is recorded.
//
// A Gate drives one attempt's session and is not safe for concurrent use: call
// Consider from the single goroutine that ranges the adapter's Stream, alongside
// the permission gate and the watch.Watcher.
//
// Known seam: only the structured edit tools are the enforcement surface. Raw
// filesystem mutation via Bash (`echo > file`) is not withheld here — Bash must
// stay Free because the agent logs *through* it — but Bash is gated to a human by
// the permission gate's ReadOnly base, so it is not an unguarded hole.
package protocol

import (
	"context"
	"fmt"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/brief"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// Prompter submits a user turn to a live session. manage.Handle satisfies it via
// its Prompt; the protocol gate holds the interface so a test can inject the
// cold-start brief without a live process.
type Prompter interface {
	Prompt(ctx context.Context, text string) error
}

// InjectBrief builds the cold-start brief for ticket/attempt and prompts it into
// the session behind p, so a freshly spawned (or resumed) agent always begins
// from the brief — the "brief is guaranteed on every cold-start" half of the
// protocol gate. It errors if the brief cannot be built or the prompt cannot be
// delivered; the caller (the reconcile loop's Admit step) runs it once, right
// after Spawn/Resume, before letting the session work.
func InjectBrief(ctx context.Context, root store.Root, ticket, attempt string, p Prompter) error {
	body, err := brief.Build(root, ticket, attempt)
	if err != nil {
		return fmt.Errorf("protocol: build brief for %s/%s: %w", ticket, attempt, err)
	}
	if err := p.Prompt(ctx, coldStartPreamble+body); err != nil {
		return fmt.Errorf("protocol: inject brief for %s/%s: %w", ticket, attempt, err)
	}
	return nil
}

// coldStartPreamble frames the injected brief so the agent treats it as the
// authoritative session context rather than a user message it might second-guess.
const coldStartPreamble = "You are cold-starting a draiver attempt. The following is your brief — the complete truth for this session: the spec, every prior decision, and any open or resolved escalation. Work the protocol: log decisions and gotchas as you go, escalate when blocked, and claim review when done.\n\n"

// InjectResumeNudge prompts a short "keep going" turn into a session that was
// *resumed* (its recorded Claude session id came back online with its context
// window intact), so the agent gets the one driving user turn a headless
// stream-json process needs to continue — without re-dumping the whole brief it
// already remembers. It is the resume-side counterpart of InjectBrief: the caller
// (the reconcile loop's Admit step) picks one by whether bringUp spawned fresh or
// resumed, keyed on the same session-identity split. It errors only if the prompt
// cannot be delivered.
func InjectResumeNudge(ctx context.Context, ticket string, p Prompter) error {
	if err := p.Prompt(ctx, resumeNudge(ticket)); err != nil {
		return fmt.Errorf("protocol: inject resume nudge for %s: %w", ticket, err)
	}
	return nil
}

// resumeNudge is the short driving turn a resumed session receives instead of the
// full cold-start brief. It states that the context is restored (so the agent
// does not expect to be re-briefed), points to `draiver brief <ticket>` as the way
// to re-ground if it needs to, and restates the standing obligation to log as it
// goes and hand off — claim review or escalate — before it stops. It deliberately
// does not re-dump the spec and log (that is drvctl-016's legitimate intent).
func resumeNudge(ticket string) string {
	return fmt.Sprintf(
		"You are resuming a draiver attempt: your session context is restored, so you are not being re-briefed. Continue the work where you left off. If you need to re-ground, run `draiver brief %s` for the current spec, every decision, and any resolved escalation. Log decisions and gotchas as you go, escalate when blocked, and claim `review` or `escalate` when the work is done.",
		ticket,
	)
}

// Decider answers a tool-permission request back to the agent. The claudecode
// adapter satisfies it via agent.Permissioner; the Gate holds the interface so a
// test can substitute a fake without a live session.
type Decider interface {
	Decide(ctx context.Context, requestID string, d agent.Decision) error
}

// Gate applies a protocol Policy to one attempt's session — the withhold-until-
// logged half. Construct it with New. It reads ticket/attempt's log to decide
// whether a justified tool's rationale is on disk, and answers the agent through
// dec only to deny (withhold); it never allows.
type Gate struct {
	root     store.Root
	ticket   string
	attempt  string
	policy   Policy
	dec      Decider
	baseline int // log tail seq at session start; a justification must be newer
}

// New returns a Gate that enforces policy on ticket/attempt's session. dec
// answers permission requests back to the agent (typically the adapter, which
// implements agent.Permissioner) and is only ever called to deny; it must be
// non-nil. New snapshots the attempt log's current tail sequence as the baseline:
// a justifying decision/gotcha must be newer than this to clear a withheld tool,
// so the rationale must be logged *during this session*, not merely present from
// an earlier one. Call New once per session, right after Spawn/Resume.
func New(root store.Root, ticket, attempt string, policy Policy, dec Decider) (*Gate, error) {
	if dec == nil {
		return nil, fmt.Errorf("protocol: decider is required")
	}
	last, ok, err := ticketlog.Last(root, ticket, attempt)
	if err != nil {
		return nil, fmt.Errorf("protocol: read log baseline for %s/%s: %w", ticket, attempt, err)
	}
	baseline := 0
	if ok {
		baseline = last.Seq
	}
	return &Gate{
		root:     root,
		ticket:   ticket,
		attempt:  attempt,
		policy:   policy,
		dec:      dec,
		baseline: baseline,
	}, nil
}

// Outcome is what Consider did with an event.
type Outcome int

const (
	// Ignored: the event was not a permission request; the Gate did nothing.
	Ignored Outcome = iota
	// Cleared: a permission request the protocol gate does not withhold — the tool
	// is Free, or a justified tool whose rationale is already on disk. The Gate did
	// not answer the agent; the caller forwards the request to the permission gate,
	// which allows or escalates it.
	Cleared
	// Withheld: a justified tool with no justifying log event yet. The Gate denied
	// the pending call with guidance to log first; the session stays live so the
	// agent can log its rationale and retry the same edit.
	Withheld
)

// Consider handles one normalized event. Non-permission events are a no-op
// (Ignored). For an agent.EventPermission it applies the policy: a Free tool, or
// a Justified tool whose rationale (a decision/gotcha newer than the session
// baseline) is on disk, is Cleared without answering the agent — the permission
// gate decides its fate. A Justified tool with no rationale yet is Withheld: the
// call is denied with guidance to log first, and the session keeps running so the
// agent can log and retry.
func (g *Gate) Consider(ctx context.Context, ev agent.Event) (Outcome, error) {
	if ev.Kind != agent.EventPermission || ev.Permission == nil {
		return Ignored, nil
	}
	req := ev.Permission

	if !g.policy.Requires(req.Tool) {
		return Cleared, nil
	}

	justified, err := g.hasRationale()
	if err != nil {
		return Ignored, fmt.Errorf("protocol: check rationale for %s: %w", req.Tool, err)
	}
	if justified {
		return Cleared, nil
	}

	if err := g.dec.Decide(ctx, req.ID, agent.Decision{
		Allow:   false,
		Message: withholdMessage(g.ticket, req.Tool),
	}); err != nil {
		return Ignored, fmt.Errorf("protocol: withhold %s: %w", req.Tool, err)
	}
	return Withheld, nil
}

// hasRationale reports whether the attempt's log holds a justifying event — a
// decision or gotcha — newer than the session baseline.
func (g *Gate) hasRationale() (bool, error) {
	events, err := ticketlog.Read(g.root, g.ticket, g.attempt)
	if err != nil {
		return false, err
	}
	for _, e := range events {
		if e.Seq <= g.baseline {
			continue
		}
		if justifies(e.Type) {
			return true, nil
		}
	}
	return false, nil
}

// justifies reports whether an event type counts as a rationale that unlocks a
// withheld edit. A decision or a gotcha justifies a change; weaker context
// (a plain note) does not.
func justifies(eventType string) bool {
	return eventType == "decision" || eventType == "gotcha"
}

// withholdMessage is the reason the agent sees when a tool is withheld — a
// transient nudge to log first, not an escalation. It names the exact command so
// the agent can unblock itself in one step.
func withholdMessage(ticket, tool string) string {
	return fmt.Sprintf(
		"Protocol gate: withholding %s until its rationale is on disk. Log the decision or gotcha that justifies this change first — `draiver log %s --type decision \"…\"` (or `--type gotcha`) — then retry; the edit proceeds once the log event exists.",
		tool, ticket,
	)
}
