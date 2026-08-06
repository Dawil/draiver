// Package completion is the supervisor's turn-end guard: the defense-in-depth
// seam that stops a supervised session from silently ending at `result: success`
// without ever handing off. Where the permission gate halts a session on a gated
// tool and the limit gate halts one running away on context, the completion gate
// reacts to a *turn ending* — the moment a headless agent goes idle — and checks
// that the session has met its obligation to either claim `review` (hand off) or
// file an `escalation` (block on a human). If it has not, the attempt would
// otherwise sit in Running + enabled forever, "done but blank" on the board
// (drvctl-022).
//
// The obligation is read from the same durable signal the protocol gate uses: a
// `review` or `escalation` newer than the session baseline (the log tail seq at
// session start). Because the recognizer promotes the agent's own
// `draiver review`/`draiver escalate` off the stream *before* the turn it ends
// on, the gate reads a log that already reflects a genuine hand-off — it never
// races the agent's own filing.
//
// It escalates in two bounded steps so it cannot loop:
//
//   - First unmet success turn: inject a one-shot nudge prompt telling the agent
//     to claim `review` or `escalate`. That nudge is itself a user turn, so the
//     agent runs again; if it files, the next turn's obligation is met and the
//     gate goes quiet.
//   - A subsequent unmet success turn (the nudge went unheeded): append a durable
//     `escalation` — which flips the attempt to Needs-me so the daemon parks it —
//     and halt the session, so a stalled "done without handoff" lands on the board
//     for a human instead of idling. The gate then latches.
//
// A Gate drives one attempt's session and is not safe for concurrent use: call
// Consider from the single goroutine that ranges the adapter's Stream, alongside
// the watch.Watcher and the other gates.
package completion

import (
	"context"
	"fmt"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// defaultMaxNudges is how many times the gate nudges an unmet success turn before
// it escalates to a human. One nudge, then escalate: a single reminder is enough
// for a cooperative agent, and more would just burn turns before the human is
// looped in.
const defaultMaxNudges = 1

// Gate enforces the hand-off-before-stop obligation on one attempt's session.
// Construct it with New. It injects the nudge prompt through nudge and, once the
// nudge is exhausted, records the escalation into ticket/attempt's log (attributed
// to actor) and reaps the session through halt.
type Gate struct {
	root     store.Root
	ticket   string
	attempt  string
	actor    string
	baseline int // log tail seq at session start; a hand-off must be newer

	nudge func(context.Context, string) error
	halt  func() error

	nudges    int  // how many nudges sent so far
	maxNudges int  // escalate once this many nudges went unheeded
	fired     bool // latched after escalating, so later frames never re-escalate
}

// New returns a Gate guarding ticket/attempt's session. nudge submits the
// reminder user turn (typically manage.Handle.Prompt) and may be nil, in which
// case the gate escalates a step sooner. halt reaps the session (typically
// manage.Handle.Kill) and may be nil, in which case the escalation is still
// recorded and the caller is left to stop the session. New snapshots the log's
// current tail as the baseline: a `review`/`escalation` must be newer than this to
// count as a hand-off from *this* session, so a stale one carried over from an
// earlier session does not satisfy the obligation. Call New once per session,
// right after Spawn/Resume.
func New(root store.Root, ticket, attempt, actor string, nudge func(context.Context, string) error, halt func() error) (*Gate, error) {
	last, ok, err := ticketlog.Last(root, ticket, attempt)
	if err != nil {
		return nil, fmt.Errorf("completion: read log baseline for %s/%s: %w", ticket, attempt, err)
	}
	baseline := 0
	if ok {
		baseline = last.Seq
	}
	return &Gate{
		root:      root,
		ticket:    ticket,
		attempt:   attempt,
		actor:     actor,
		baseline:  baseline,
		nudge:     nudge,
		halt:      halt,
		maxNudges: defaultMaxNudges,
	}, nil
}

// Outcome is what Consider did with an event.
type Outcome int

const (
	// Ignored: the event was not a successful turn end, or the obligation was
	// already met (a review/escalation is on disk). The gate did nothing.
	Ignored Outcome = iota
	// Nudged: a successful turn ended with nothing filed; the gate injected the
	// reminder prompt telling the agent to claim review or escalate.
	Nudged
	// Escalated: a successful turn ended with nothing filed after the nudge was
	// exhausted; the gate recorded a human-facing escalation and (if halt was set)
	// reaped the session. The gate then latches.
	Escalated
)

// Consider inspects one normalized event. Only a turn ending in success is acted
// on — an error or interrupted turn is not a false "done" and is Ignored, as is
// every non-turn-end event and any success turn whose obligation is already met.
// The first unmet success turn is Nudged; a later unmet success turn (after the
// nudge budget is spent) is Escalated and the session halted. Once Escalated the
// gate latches, so the remaining in-flight frames before the reap lands do not
// stack duplicate escalations.
func (g *Gate) Consider(ctx context.Context, ev agent.Event) (Outcome, error) {
	if g.fired || ev.Kind != agent.EventTurnEnd || ev.Turn != "success" {
		return Ignored, nil
	}

	met, err := g.handedOff()
	if err != nil {
		return Ignored, fmt.Errorf("completion: check hand-off for %s/%s: %w", g.ticket, g.attempt, err)
	}
	if met {
		return Ignored, nil
	}

	// A nil nudger has nothing to deliver, so skip straight to escalation rather
	// than burn a silent nudge budget.
	if g.nudge != nil && g.nudges < g.maxNudges {
		g.nudges++
		if err := g.nudge(ctx, nudgeMessage(g.ticket)); err != nil {
			return Ignored, fmt.Errorf("completion: nudge %s/%s: %w", g.ticket, g.attempt, err)
		}
		return Nudged, nil
	}

	// The nudge went unheeded; hand the stall to a human. Latch first so even a
	// failing append or halt cannot re-enter and stack a second escalation on the
	// next frame.
	g.fired = true
	if _, err := ticketlog.Append(g.root, g.ticket, g.attempt, event.Event{
		Type:  "escalation",
		Actor: g.actor,
		Body:  escalationBody(),
	}); err != nil {
		return Ignored, fmt.Errorf("completion: record stall escalation for %s/%s: %w", g.ticket, g.attempt, err)
	}
	if g.halt != nil {
		if err := g.halt(); err != nil {
			// The escalation is already durable; report the halt failure but keep the
			// Escalated outcome so the caller knows the attempt is now blocked.
			return Escalated, fmt.Errorf("completion: halt after stall escalation: %w", err)
		}
	}
	return Escalated, nil
}

// handedOff reports whether a review or escalation newer than the session
// baseline is on disk — the durable proof the session met its hand-off
// obligation. It reads the same log tail the protocol gate reads, so a hand-off
// the recognizer just promoted off the stream is seen immediately.
func (g *Gate) handedOff() (bool, error) {
	events, err := ticketlog.Read(g.root, g.ticket, g.attempt)
	if err != nil {
		return false, err
	}
	for _, e := range events {
		if e.Seq <= g.baseline {
			continue
		}
		if e.Type == "review" || e.Type == "escalation" {
			return true, nil
		}
	}
	return false, nil
}

// nudgeMessage is the one-shot reminder a session that stopped without handing off
// receives. It names the exact commands so the agent can meet the obligation in
// one step, mirroring the withhold nudge's shape.
func nudgeMessage(ticket string) string {
	return fmt.Sprintf(
		"Your turn ended at success, but no `review` or `escalation` has been filed for this attempt — a supervised session must hand off before it stops. If the work is complete, run `draiver review %s` to claim it for review. If you are blocked or need a human decision, run `draiver escalate %s \"…\"`. Do one now.",
		ticket, ticket,
	)
}

// escalationBody renders the human-facing escalation for a session that reached
// success without handing off and did not respond to the nudge (the board renders
// event bodies as Markdown).
func escalationBody() string {
	return "This session reached a successful stopping point but never filed a `review` or an `escalation`, and did not claim one after being nudged. The supervisor halted it so the stalled attempt lands on the board for a human instead of idling in Running forever.\n\n" +
		"Resolve this escalation to resume the attempt — it comes back up with its session context restored — after redirecting it as needed, or reopen it for review if the work is in fact complete."
}
