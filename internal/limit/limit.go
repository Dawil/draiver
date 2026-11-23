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
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/handoff"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// TagContextAutoStop marks the escalation the limit gate records so a consumer
// can recognize a context-window auto-stop without sniffing stopBody's prose (the
// opaque, producer-owned Tags field, drv-026). The web board keys the "Restart
// context window" affordance off this tag (drv-028); the fold and brief treat it
// as any other retrieval label, so tagging is truth-neutral.
const TagContextAutoStop = "context-auto-stop"

// TagContextAutoRestart marks the `resolution` the gate appends when it resolves
// its own auto-stop escalation after a successful handoff and restarts the attempt
// without a human (drvctl-061). Counting these on an attempt's log is the restart
// budget: the durable count survives a daemon restart, so a second trip on the same
// attempt escalates to a human even across ctl up restarts.
const TagContextAutoRestart = "context-auto-restart"

// Handoff configures the checkpoint-before-reap (drvctl-061). The zero value — or a
// Grace <= 0 or a nil Session — disables it, and the gate halts the moment the limit
// is crossed exactly as before.
type Handoff struct {
	// Grace bounds how long the gate waits for the session's handoff note before it
	// reaps anyway (config stop_grace_seconds).
	Grace time.Duration
	// Session is the live session to interrupt and prompt (manage.Handle).
	Session handoff.Session
	// AutoRestarts is how many times this attempt may be restarted without a human
	// after a successful handoff (config context_auto_restarts). 0 never restarts:
	// the escalation stays open for a human, as before.
	AutoRestarts int
	// Restart prepares the attempt for a fresh cold-start before the halt —
	// typically discarding the recorded session id so the next bring-up spawns a
	// new session from the brief (the same flush as `ctl restart --new-session`).
	// It runs only when the gate resolves its own escalation. Nil skips the flush.
	Restart func() error
	// Report, when set, receives the checkpoint's outcome once it is decided, for
	// the caller's system log. It is called from the checkpoint goroutine.
	Report func(Report)
	// Ctx bounds the checkpoint (the daemon's ingest context). Nil means
	// context.Background().
	Ctx context.Context
	// Poll overrides handoff.DefaultPoll; tests shorten it.
	Poll time.Duration
}

// Report is what one auto-stop checkpoint did.
type Report struct {
	Escalation int            // the auto-stop escalation's seq
	Result     handoff.Result // how the checkpoint ended
	Restarted  bool           // the gate resolved its escalation and the attempt restarts
	Resolution int            // the supervisor's resolution seq, when Restarted
	Restarts   int            // auto-restarts already spent on this attempt before this trip
	HaltErr    error          // the reap's error, if any
}

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
	handoff   Handoff

	fired bool // fire at most once — later frames on the same session are ignored

	mu   sync.Mutex
	ckpt *handoff.Checkpoint // non-nil while the checkpoint runs
	done chan struct{}       // closed when the checkpoint (and its reap) finished
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

// WithHandoff enables the checkpoint-before-reap (drvctl-061) and returns g.
func (g *Gate) WithHandoff(h Handoff) *Gate {
	g.handoff = h
	return g
}

func (g *Gate) checkpointEnabled() bool {
	return g.handoff.Grace > 0 && g.handoff.Session != nil
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
	// Checkpointing: the context fill crossed the threshold (or had already), the
	// escalation is recorded, and the handoff checkpoint is running — the session
	// stays live until it lands its note or the grace period runs out, then the gate
	// reaps it. The caller keeps consuming the stream (so the agent's `draiver log`
	// permission ask is still answered) but skips the turn-end completion gate and
	// the human-interrupt hold, both of which the checkpoint's own interrupt and
	// handoff turn would otherwise trip.
	Checkpointing
)

// Consider inspects one normalized event. Any event that carries a usage frame
// with a live context reading (ContextTokens > 0) is measured against the
// threshold; everything else is a no-op (Ignored). The first frame to cross the
// threshold records a durable escalation and halts the session (Stopped); the
// gate then latches, so the remaining in-flight frames before the reap lands do
// not record duplicate escalations. The escalation is written before the halt so
// the durable record survives a failed reap.
//
// With a Handoff configured, the crossing instead starts the checkpoint
// (Checkpointing) and every later event is fed to it — still Checkpointing — until
// the checkpoint has reaped the session.
func (g *Gate) Consider(ev agent.Event) (Outcome, error) {
	if ck := g.active(); ck != nil {
		ck.Observe(ev)
		return Checkpointing, nil
	}
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

	var ck *handoff.Checkpoint
	if g.checkpointEnabled() {
		// Mark the checkpoint live *before* the escalation lands: the escalation flips
		// the attempt to Needs-me, and the reconcile tick must already see a run that
		// is mid-checkpoint (and leave it alone) by the time it sees Needs-me.
		ck = handoff.New(g.root, g.ticket, g.attempt, g.handoff.Session, g.handoff.Grace)
		if g.handoff.Poll > 0 {
			ck.Poll = g.handoff.Poll
		}
		g.mu.Lock()
		g.ckpt = ck
		g.done = make(chan struct{})
		g.mu.Unlock()
	}

	esc, err := ticketlog.Append(g.root, g.ticket, g.attempt, event.Event{
		Type:  "escalation",
		Actor: g.actor,
		Body:  stopBody(ctxTokens, g.threshold, ck != nil),
		Tags:  []string{TagContextAutoStop},
	})
	if err != nil {
		if ck != nil {
			g.finish()
		}
		return Ignored, fmt.Errorf("limit: record auto-stop for %s/%s: %w", g.ticket, g.attempt, err)
	}

	if ck != nil {
		// A frame that is itself the turn end means no turn is in flight; anything
		// else crossed mid-turn and the checkpoint interrupts it first.
		inFlight := ev.Kind != agent.EventTurnEnd
		go g.checkpoint(ck, esc.Seq, inFlight)
		return Checkpointing, nil
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

// Checkpointing reports whether the handoff checkpoint is running — the session is
// past the limit but deliberately still live. The reconcile tick reads it to leave
// the run alone instead of retiring it mid-handoff.
func (g *Gate) Checkpointing() bool { return g.active() != nil }

// Wait blocks until a running checkpoint has finished (and reaped), or returns at
// once when none is running.
func (g *Gate) Wait() {
	g.mu.Lock()
	done := g.done
	g.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (g *Gate) active() *handoff.Checkpoint {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ckpt
}

func (g *Gate) finish() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ckpt = nil
	if g.done != nil {
		select {
		case <-g.done:
		default:
			close(g.done)
		}
	}
}

// checkpoint runs the handoff, decides whether the attempt restarts on its own,
// and reaps. The order is the drvctl-061 contract: the escalation is already
// durable (Consider wrote it); a restart's resolution and session flush land
// *before* the halt, while the run is still marked checkpointing, so the reconcile
// tick never observes a reaped-but-Needs-me run it would retire (and whose
// imperative start marker it would sweep); then the halt; then the checkpointing
// mark clears, and the spent run (pid 0, Running) is re-admitted fresh.
func (g *Gate) checkpoint(ck *handoff.Checkpoint, escSeq int, inFlight bool) {
	defer g.finish()
	ctx := g.handoff.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	res := ck.Run(ctx, inFlight, "its context window reached the configured limit")
	rep := Report{Escalation: escSeq, Result: res, Restarts: g.restartsSpent()}

	if res.Outcome == handoff.Written && rep.Restarts < g.handoff.AutoRestarts {
		if seq, err := g.resolve(escSeq, res, rep.Restarts+1); err == nil {
			rep.Restarted, rep.Resolution = true, seq
			if g.handoff.Restart != nil {
				_ = g.handoff.Restart()
			}
		}
	}
	if g.halt != nil {
		rep.HaltErr = g.halt()
	}
	if g.handoff.Report != nil {
		g.handoff.Report(rep)
	}
}

// restartsSpent counts the supervisor's earlier auto-restart resolutions on this
// attempt — the durable restart budget.
func (g *Gate) restartsSpent() int {
	evs, err := ticketlog.Read(g.root, g.ticket, g.attempt)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range evs {
		if e.Type == "resolution" && slices.Contains(e.Tags, TagContextAutoRestart) {
			n++
		}
	}
	return n
}

// resolve appends the supervisor's resolution of its own auto-stop escalation.
func (g *Gate) resolve(escSeq int, res handoff.Result, n int) (int, error) {
	e, err := ticketlog.Append(g.root, g.ticket, g.attempt, event.Event{
		Type:  "resolution",
		Actor: g.actor,
		Refs:  []int{escSeq},
		Tags:  []string{TagContextAutoRestart},
		Body: fmt.Sprintf("Auto-restart %d of %d: the session logged its handoff (note #%d) before the reap, so the supervisor "+
			"restarts the attempt on a fresh context window, cold-started from the brief. If this attempt hits the limit again "+
			"past its auto-restart budget, it escalates to a human (`context_auto_restarts`).",
			n, g.handoff.AutoRestarts, res.NoteSeq),
	})
	if err != nil {
		return 0, err
	}
	return e.Seq, nil
}

// stopBody renders the human-facing auto-stop escalation as Markdown (the board
// renders event bodies as Markdown).
func stopBody(ctxTokens, threshold int, checkpoint bool) string {
	body := fmt.Sprintf(
		"Context-window auto-stop: the session's context fill reached **%s tokens**, "+
			"crossing the configured limit of **%s**. The supervisor halted it to keep a "+
			"runaway session from burning tokens with no human in the loop.\n\n"+
			"Resolve this escalation to resume the attempt — it comes back up from a fresh "+
			"cold-start brief (a clean context window). If the work genuinely needs a larger "+
			"window, raise `context_limit` in the draiver config first.",
		commas(ctxTokens), commas(threshold),
	)
	if checkpoint {
		body += "\n\nBefore the reap the supervisor asks the session to log a handoff note (`--tag handoff`) so the next " +
			"session picks up where this one stopped. If it does and the attempt has auto-restarts left " +
			"(`context_auto_restarts`), the supervisor resolves this escalation itself and restarts the attempt."
	}
	return body
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
