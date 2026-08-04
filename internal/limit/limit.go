// Package limit is the supervisor's context-window backstop: the third
// enforcement seam alongside the protocol gate and the permission gate. Where the
// permission gate halts a session on a gated tool, the limit gate halts one that
// is about to run away on context — a session whose live context-window fill
// crosses a configured threshold is stopped before it keeps burning tokens with
// no human in the loop (drvctl-012).
//
// It reads the same live signal the meter and `ctl status` surface — a usage
// frame's ContextTokens, the size of the current request's prompt. (That signal
// is only trustworthy because the adapter/meter now feed the gauge from
// per-request frames, not the cumulative turn-end aggregate that produced the
// drvctl-009 "2M context" artifact.) When the fill crosses the threshold it does
// what the permission gate does on an escalate: append a durable `escalation`
// event — which flips the attempt to Needs-me so the daemon parks it for a human
// instead of re-admitting it into the same runaway — then reap the session. The
// escalation is written before the halt, so the record survives even if the reap
// fails, and a later `resolve` resumes the attempt from a fresh cold-start brief
// (a clean context window).
//
// A Gate drives one attempt's session and is not safe for concurrent use: call
// Consider from the single goroutine that ranges the adapter's Stream, alongside
// the watch.Watcher and the other gates. It fires at most once per session.
package limit

import (
	"fmt"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// Gate enforces a context-window ceiling on one attempt's session. Construct it
// with New. It appends the auto-stop escalation into ticket/attempt's log
// (attributing it to actor) and reaps the session through halt.
type Gate struct {
	root      store.Root
	ticket    string
	attempt   string
	actor     string
	threshold int
	halt      func() error

	fired bool // fire at most once — later frames on the same session are ignored
}

// New returns a Gate that stops ticket/attempt's session when a usage frame's
// ContextTokens reaches threshold. halt reaps the session (typically
// manage.Handle.Kill); it may be nil, in which case the escalation is still
// recorded and the caller is left to stop the session. A threshold <= 0 disables
// the gate: Consider then never fires.
func New(root store.Root, ticket, attempt, actor string, threshold int, halt func() error) *Gate {
	return &Gate{
		root:      root,
		ticket:    ticket,
		attempt:   attempt,
		actor:     actor,
		threshold: threshold,
		halt:      halt,
	}
}

// Outcome is what Consider did with an event.
type Outcome int

const (
	// Ignored: the event carried no context signal, was under the threshold, the
	// gate is disabled, or the gate had already fired.
	Ignored Outcome = iota
	// Stopped: the context fill crossed the threshold; an escalation was recorded
	// and (if halt was set) the session reaped.
	Stopped
)

// Consider inspects one normalized event. Any event that carries a usage frame
// with a live context reading (ContextTokens > 0) is measured against the
// threshold; everything else is a no-op (Ignored). The first frame to cross the
// threshold records a durable escalation and halts the session (Stopped); the
// gate then latches, so the remaining in-flight frames before the reap lands do
// not record duplicate escalations. The escalation is written before the halt so
// the durable record survives a failed reap.
func (g *Gate) Consider(ev agent.Event) (Outcome, error) {
	if g.fired || g.threshold <= 0 || ev.Usage == nil {
		return Ignored, nil
	}
	ctxTokens := ev.Usage.ContextTokens
	if ctxTokens < g.threshold {
		return Ignored, nil
	}

	// Latch before doing anything else: even if the append or halt errors, we must
	// not re-enter and stack duplicate escalations on the next frame.
	g.fired = true

	if _, err := ticketlog.Append(g.root, g.ticket, g.attempt, event.Event{
		Type:  "escalation",
		Actor: g.actor,
		Body:  stopBody(ctxTokens, g.threshold),
	}); err != nil {
		return Ignored, fmt.Errorf("limit: record auto-stop for %s/%s: %w", g.ticket, g.attempt, err)
	}

	if g.halt != nil {
		if err := g.halt(); err != nil {
			// The escalation is already durable; report the halt failure but keep the
			// Stopped outcome so the caller knows the attempt is now blocked.
			return Stopped, fmt.Errorf("limit: halt after auto-stop: %w", err)
		}
	}
	return Stopped, nil
}

// stopBody renders the human-facing auto-stop escalation as Markdown (the board
// renders event bodies as Markdown).
func stopBody(ctxTokens, threshold int) string {
	return fmt.Sprintf(
		"Context-window auto-stop: the session's context fill reached **%s tokens**, "+
			"crossing the configured limit of **%s**. The supervisor halted it to keep a "+
			"runaway session from burning tokens with no human in the loop.\n\n"+
			"Resolve this escalation to resume the attempt — it comes back up from a fresh "+
			"cold-start brief (a clean context window). If the work genuinely needs a larger "+
			"window, raise `context_limit` in the draiver config first.",
		commas(ctxTokens), commas(threshold),
	)
}

// commas groups an integer into thousands for a readable figure (135520 ->
// 135,520). Kept local to the package so the gate has no presentation dependency.
func commas(n int) string {
	if n < 0 {
		return "-" + commas(-n)
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return commas(n/1000) + fmt.Sprintf(",%03d", n%1000)
}
