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
// `park` is the third legitimate hand-off (drvctl-052): a turn-end that suspends
// the attempt to Pending with no human-facing escalation. The gate accepts a
// `park` exactly when the attempt still has a pending wake source
// (internal/wake) — a parkable state is also what lets the gate *suspend rather
// than escalate* a stopping coordinator that delegated to a child via
// `resolve --by` (which writes the child's log, not its own). Without a wake
// source a park would sleep forever, so it is not accepted and the gate falls
// through to the nudge/escalate path.
//
// It escalates in two bounded steps so it cannot loop:
//
//   - First unmet success turn: inject a one-shot nudge prompt telling the agent
//     to claim `review`, `park`, or `escalate`. That nudge is itself a user turn,
//     so the agent runs again; if it files, the next turn's obligation is met and
//     the gate goes quiet.
//   - A subsequent unmet success turn (the nudge went unheeded): if the attempt is
//     parkable (a wake source exists) the gate SUSPENDS it — no escalation, the
//     wake machinery reaps it to Pending — because a stopping, still-wakeable
//     attempt is not a stall on the human's desk. Otherwise it appends a durable
//     `escalation` — which flips the attempt to Needs-me so the daemon parks it —
//     and halts the session, so a stalled "done without handoff" lands on the board
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
	"github.com/Dawil/draiver/internal/wake"
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
	// reminder prompt telling the agent to claim review, park, or escalate.
	Nudged
	// Escalated: a successful turn ended with nothing filed after the nudge was
	// exhausted and the attempt had no wake source; the gate recorded a human-facing
	// escalation and (if halt was set) reaped the session. The gate then latches.
	Escalated
	// Parked: a successful turn ended without a hand-off after the nudge was
	// exhausted, but the attempt still has a pending wake source (internal/wake), so
	// it is in a legitimate suspend state rather than a stall. The gate declined to
	// manufacture a human escalation — the wake machinery (the coordinator gate in
	// drvctl-052, the async waker in drvctl-055) reaps it to Pending — and latches.
	// An explicit `draiver park` hand-off is Ignored (quiet, like review/escalate);
	// Parked is only the gate's own suppression of an otherwise-manufactured escalation.
	Parked
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

	// A pending wake source makes this a parkable suspend point: a `park` counts as a
	// hand-off, and an unmet turn is suspended (Parked) rather than escalated onto a
	// human. Read the sources once per considered turn-end — the kinds matter for the
	// auto-park below, not only whether any exist.
	sources, err := wake.Pending(g.root, g.ticket, g.attempt)
	if err != nil {
		return Ignored, fmt.Errorf("completion: check wake source for %s/%s: %w", g.ticket, g.attempt, err)
	}
	parkable := len(sources) > 0

	met, err := g.handedOff(parkable)
	if err != nil {
		return Ignored, fmt.Errorf("completion: check hand-off for %s/%s: %w", g.ticket, g.attempt, err)
	}
	if met {
		return Ignored, nil
	}

	// A nil nudger has nothing to deliver, so skip straight to the escalate/suspend
	// decision rather than burn a silent nudge budget.
	if g.nudge != nil && g.nudges < g.maxNudges {
		g.nudges++
		if err := g.nudge(ctx, nudgeMessage(g.ticket, parkable)); err != nil {
			return Ignored, fmt.Errorf("completion: nudge %s/%s: %w", g.ticket, g.attempt, err)
		}
		return Nudged, nil
	}

	// The nudge went unheeded. A still-wakeable attempt that stopped is suspending,
	// not stalling: do not manufacture a human escalation (that is the pre-digest
	// net-negative bug — a coordinator that delegated via `resolve --by` and stopped
	// escalated onto its own ticket). The wake machinery reaps it to Pending. Latch
	// so later frames settle on the one decision.
	if parkable {
		g.fired = true
		// The safety net for drvctl-055's emergent pattern: a worker that launched a
		// detached test and ended its turn without parking (forgot the `&& draiver
		// park`) has a live async job but no explicit park, so the reconciler's
		// async-dormancy — which keys on a park after the launch — would never reap it
		// and the idle session would linger. Author the park the agent omitted so the
		// suspend is durable and the async wake machinery reaps it to Pending. Scoped to
		// the async source: a `wants:` coordinator is reaped by the coordinator gate and
		// must keep its existing no-write Parked behaviour (drvctl-052).
		if hasAsyncSource(sources) {
			if _, err := ticketlog.Append(g.root, g.ticket, g.attempt, event.Event{
				Type:  "park",
				Actor: g.actor,
				Body:  autoParkBody(),
			}); err != nil {
				return Parked, fmt.Errorf("completion: auto-park %s/%s: %w", g.ticket, g.attempt, err)
			}
		}
		return Parked, nil
	}

	// The nudge went unheeded and nothing can wake it; hand the stall to a human. Latch first so even a
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

// handedOff reports whether a hand-off newer than the session baseline is on disk
// — the durable proof the session met its obligation. A `review` or `escalation`
// always counts; a `park` counts only when parkable, because a park with no wake
// source would sleep forever and so is not a legitimate suspend (it falls through
// to the nudge/escalate path). It reads the same log tail the protocol gate reads,
// so a hand-off the recognizer just promoted off the stream is seen immediately.
func (g *Gate) handedOff(parkable bool) (bool, error) {
	events, err := ticketlog.Read(g.root, g.ticket, g.attempt)
	if err != nil {
		return false, err
	}
	sawPark := false
	for _, e := range events {
		if e.Seq <= g.baseline {
			continue
		}
		switch e.Type {
		case "review", "escalation":
			return true, nil
		case "park":
			sawPark = true
		}
	}
	return sawPark && parkable, nil
}

// hasAsyncSource reports whether any of the attempt's pending wake sources is a
// detached async job (drvctl-055) — the kind the gate auto-parks for, so a worker
// that launched a test but omitted `draiver park` still suspends durably rather
// than lingering idle.
func hasAsyncSource(sources []wake.Source) bool {
	for _, s := range sources {
		if s.Kind == wake.AsyncJob {
			return true
		}
	}
	return false
}

// autoParkBody renders the park the gate authors on an agent's behalf when it
// stopped with a live async job but did not hand off (the board renders event
// bodies as Markdown).
func autoParkBody() string {
	return "Auto-parked by the completion gate: this turn ended with a detached test still running but no `review`, `park`, or `escalation` filed. Suspending to Pending — the test result will land on this log and wake the attempt to claim review (green) or iterate (red)."
}

// nudgeMessage is the one-shot reminder a session that stopped without handing off
// receives. It names the exact commands so the agent can meet the obligation in
// one step, mirroring the withhold nudge's shape. When the attempt is parkable (a
// wake source exists) it offers `park` as the stop option alongside escalate,
// because a coordinator mid-epic must not claim its own review.
func nudgeMessage(ticket string, parkable bool) string {
	if parkable {
		return fmt.Sprintf(
			"Your turn ended at success, but no `review`, `park`, or `escalation` has been filed for this attempt — a supervised session must hand off before it stops. If nothing needs you right now and a wake source will bring you back (a child's lifecycle, or an async job), run `draiver park %s` to suspend to Pending. If you are blocked or need a human decision, run `draiver escalate %s \"…\"`. Do one now.",
			ticket, ticket,
		)
	}
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
