// Package handoff is the supervisor's checkpoint-before-reap (drvctl-061): the one
// step both stop paths run before they kill a live session, so what the agent knew
// about where it was up to survives the reap.
//
// A stop used to be abrupt. The context-limit gate (internal/limit) and `ctl stop`
// both reaped the process the moment they decided to, and the next session had to
// rediscover the in-flight work from whatever note happened to be last (drv-045 and
// drv-046 each lost tens of thousands of tokens that way). A note written at an
// early warning goes stale as work continues, so the handoff happens at the moment
// of the stop instead:
//
//  1. If a turn is in flight, interrupt it (agent.Interrupt).
//  2. Prompt the live session to run
//     `draiver log <ticket> --type note --tag handoff "…"` with a structured note
//     (Done / In progress / Uncommitted files / Gotchas / Don't redo).
//  3. Wait up to the grace period for a new `note` tagged `handoff` to land on the
//     attempt's log. A handoff turn that ends with no note counts as a timeout.
//
// The caller then reaps. A Checkpoint never kills anything itself — it only asks
// and watches — so every ordering guarantee (escalation before halt, interrupt hold
// before reap) stays with the caller.
//
// The note is written by the agent through its own `draiver log`, so it is
// attributed to the session actor (drvctl-060) and shows up in the next brief like
// any other live fact.
package handoff

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// Tag marks the handoff note the session logs. It is the opaque retrieval label the
// checkpoint watches for (and a later `recall --tag handoff` finds).
const Tag = "handoff"

// DefaultPoll is how often Run re-reads the attempt log for the handoff note.
const DefaultPoll = 250 * time.Millisecond

// Session is the slice of a live session the checkpoint drives — satisfied by
// manage.Handle.
type Session interface {
	Interrupt(ctx context.Context) error
	Prompt(ctx context.Context, text string) error
}

// Outcome is how a checkpoint ended.
type Outcome string

const (
	// Written: the session logged a handoff note within the grace period.
	Written Outcome = "written"
	// TimedOut: the grace period elapsed, or the handoff turn ended, with no note.
	TimedOut Outcome = "timed-out"
	// Failed: the handoff prompt could not be delivered to the session.
	Failed Outcome = "failed"
	// Cancelled: the caller's context ended before the checkpoint finished.
	Cancelled Outcome = "cancelled"
)

// Result reports a finished checkpoint.
type Result struct {
	Outcome Outcome
	// NoteSeq is the handoff note's seq on the attempt log when Outcome is Written.
	NoteSeq int
	// TurnEnded is true when a TimedOut came from the handoff turn ending without a
	// note, rather than from the grace period running out.
	TurnEnded bool
	// Elapsed is how long the checkpoint took, from the prompt to the outcome.
	Elapsed time.Duration
	// Err is the delivery failure behind a Failed outcome.
	Err error
}

// Describe renders the outcome as one human-readable line for a system event or a
// resolution body.
func (r Result) Describe() string {
	switch r.Outcome {
	case Written:
		return fmt.Sprintf("handoff note #%d written in %s", r.NoteSeq, r.Elapsed.Round(time.Second))
	case TimedOut:
		if r.TurnEnded {
			return fmt.Sprintf("handoff turn ended without a note after %s", r.Elapsed.Round(time.Second))
		}
		return fmt.Sprintf("no handoff note within the grace period (%s)", r.Elapsed.Round(time.Second))
	case Failed:
		return fmt.Sprintf("handoff prompt could not be delivered: %v", r.Err)
	case Cancelled:
		return "handoff checkpoint cancelled (supervisor shutting down)"
	}
	return string(r.Outcome)
}

// Checkpoint is one checkpoint-before-reap against one attempt's live session.
// Construct it with New, feed it the session's stream events with Observe (from the
// ingest goroutine) while Run (on another goroutine) asks for and waits on the note.
type Checkpoint struct {
	root    store.Root
	ticket  string
	attempt string
	sess    Session
	grace   time.Duration

	// Poll overrides DefaultPoll; tests shorten it.
	Poll time.Duration

	ends chan struct{} // one send per observed top-level turn end
}

// New returns a Checkpoint for ticket/attempt's session, waiting at most grace for
// the handoff note.
func New(root store.Root, ticket, attempt string, sess Session, grace time.Duration) *Checkpoint {
	return &Checkpoint{
		root:    root,
		ticket:  ticket,
		attempt: attempt,
		sess:    sess,
		grace:   grace,
		ends:    make(chan struct{}, 8),
	}
}

// Observe feeds one stream event to the checkpoint. Only top-level turn ends
// matter (a background task's completion turn, Origin != "", is not the handoff
// turn). It never blocks, so the ingest goroutine can call it on every event.
func (c *Checkpoint) Observe(ev agent.Event) {
	if ev.Kind != agent.EventTurnEnd || ev.Origin != "" {
		return
	}
	select {
	case c.ends <- struct{}{}:
	default:
	}
}

// Run performs the checkpoint: interrupt the in-flight turn (when inFlight),
// prompt the session for the handoff note, and wait for it. reason names why the
// session is stopping, for the prompt ("the context window reached its limit").
//
// A turn that was in flight ends first (interrupted) and the handoff turn second,
// so with inFlight the second observed turn end without a note is the timeout; with
// no turn in flight, the first one is.
func (c *Checkpoint) Run(ctx context.Context, inFlight bool, reason string) Result {
	baseline := tail(c.root, c.ticket, c.attempt)
	if inFlight {
		// Best-effort: an interrupt that fails still lets the prompt queue behind the
		// running turn, and the grace period bounds the wait either way.
		_ = c.sess.Interrupt(ctx)
	}
	start := time.Now()
	if err := c.sess.Prompt(ctx, PromptText(c.ticket, reason, c.grace)); err != nil {
		return Result{Outcome: Failed, Err: err, Elapsed: time.Since(start)}
	}

	poll := c.Poll
	if poll <= 0 {
		poll = DefaultPoll
	}
	expect := 1
	if inFlight {
		expect = 2
	}
	deadline := time.NewTimer(c.grace)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	written := func() (Result, bool) {
		if seq, ok := FindNote(c.root, c.ticket, c.attempt, baseline); ok {
			return Result{Outcome: Written, NoteSeq: seq, Elapsed: time.Since(start)}, true
		}
		return Result{}, false
	}
	seen := 0
	for {
		if r, ok := written(); ok {
			return r
		}
		select {
		case <-ctx.Done():
			if r, ok := written(); ok {
				return r
			}
			return Result{Outcome: Cancelled, Elapsed: time.Since(start)}
		case <-deadline.C:
			if r, ok := written(); ok {
				return r
			}
			return Result{Outcome: TimedOut, Elapsed: time.Since(start)}
		case <-c.ends:
			seen++
			if seen >= expect {
				// The agent's `draiver log` lands before its turn ends, so one final
				// read settles it.
				if r, ok := written(); ok {
					return r
				}
				return Result{Outcome: TimedOut, TurnEnded: true, Elapsed: time.Since(start)}
			}
		case <-ticker.C:
		}
	}
}

// FindNote returns the seq of the first `note` tagged Tag on ticket/attempt's log
// with a seq above after.
func FindNote(root store.Root, ticket, attempt string, after int) (int, bool) {
	evs, err := ticketlog.Read(root, ticket, attempt)
	if err != nil {
		return 0, false
	}
	for _, e := range evs {
		if e.Seq > after && e.Type == "note" && slices.Contains(e.Tags, Tag) {
			return e.Seq, true
		}
	}
	return 0, false
}

// tail is the attempt log's highest seq (0 when unreadable).
func tail(root store.Root, ticket, attempt string) int {
	evs, err := ticketlog.Read(root, ticket, attempt)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range evs {
		n = max(n, e.Seq)
	}
	return n
}

// PromptText is the user turn the checkpoint sends: stop the task, log one
// structured handoff note, nothing else.
func PromptText(ticket, reason string, grace time.Duration) string {
	return fmt.Sprintf(`**Stop — hand off now.** The supervisor is about to end this session: %s. Do not continue the task, commit, or run tests. Your only job this turn is to leave a handoff note so a fresh session can carry on exactly where you are.

You may run `+"`git status --short`"+` first. Then run exactly this one command, filling in each section briefly:

`+"```"+`
draiver log %s --type note --tag %s "$(cat <<'MD'
Handoff: <one line — where the work stands>

## Done
- …

## In progress
- … — next concrete step: …

## Uncommitted files
- …

## Gotchas
- … (slow commands, traps)

## Don't redo
- …
MD
)"
`+"```"+`

You have about %s. After the command succeeds, stop — reply with one line and end your turn.`,
		reason, ticket, Tag, grace.Round(time.Second))
}

// Reserve is the context headroom (in tokens) the handoff turn is assumed to need
// above the auto-stop threshold. One tool result can overshoot the limit by tens of
// thousands of tokens (drv-046 reached 201.5K; a single read added 31K), and the
// handoff turn itself has to fit after that.
const Reserve = 40_000

// HeadroomWarning returns a warning when contextLimit + Reserve exceeds the model's
// context window — the handoff turn may then find no room left — or "" when there
// is headroom, the limit is disabled, or the window is unknown (window <= 0).
func HeadroomWarning(contextLimit, window int) string {
	if contextLimit <= 0 || window <= 0 || contextLimit+Reserve <= window {
		return ""
	}
	return fmt.Sprintf("handoff headroom: context_limit (%d) + a %d-token handoff reserve exceeds context_window (%d); "+
		"a session stopped at the limit may have no room left to write its handoff note — lower context_limit to %d or less",
		contextLimit, Reserve, window, window-Reserve)
}
