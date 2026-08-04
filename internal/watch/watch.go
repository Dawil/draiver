// Package watch is the supervisor's "Watch" step: it consumes one session's
// normalized event stream and does the two jobs the reconcile loop's step 2
// names — promote and meter.
//
//   - Promote: recognize the semantic protocol events an agent emits
//     (gotcha / decision / escalation / review) and append them to the attempt's
//     durable, hash-chained log via internal/ticketlog. The supervisor owns the
//     append, so it is the single writer of the log.
//   - Meter: tee every raw stream-json line to the session journal and fold
//     token, cost, and context-window usage into meter.json — the live gauge the
//     UI surfaces so a human can see a session approaching context exhaustion.
//
// A Watcher drives one session and is not safe for concurrent use: run its
// Ingest (or repeated Process calls) from a single goroutine, matching the one
// goroutine that ranges an adapter's Stream.
package watch

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/session"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// Watcher consumes a session's stream for one attempt. Construct it with New.
type Watcher struct {
	root    store.Root
	ticket  string
	attempt string
	actor   string // identity stamped on promoted events, e.g. "agent:claude-code"

	sess *session.Store
	rec  Recognizer
	now  func() time.Time

	// lastRaw is the previous raw stream-json line teed to the journal. One
	// stream-json line normalizes into several agent.Events that all carry the
	// same Raw, so teeing per event would duplicate the line; dedupe consecutive
	// identical Raw to write each source line exactly once.
	lastRaw []byte
}

// New returns a Watcher that promotes into ticket/attempt's log (attributing
// promoted events to actor), tees and meters through sess, and recognizes
// semantic events with rec. sess and rec must be non-nil.
func New(root store.Root, ticket, attempt, actor string, sess *session.Store, rec Recognizer) *Watcher {
	return &Watcher{
		root:    root,
		ticket:  ticket,
		attempt: attempt,
		actor:   actor,
		sess:    sess,
		rec:     rec,
		now:     time.Now,
	}
}

// Ingest consumes events until the channel is closed (the session exited) or ctx
// is cancelled, applying Process to each. It returns nil on a clean drain, ctx's
// error on cancellation, or the first Process error.
func (w *Watcher) Ingest(ctx context.Context, events <-chan agent.Event) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if _, err := w.Process(ev); err != nil {
				return err
			}
		}
	}
}

// Process handles one normalized event: tee its raw line to the journal, fold
// any usage into the meter, and promote it to the durable log if the recognizer
// claims it. It reports whether the event was promoted. Not safe for concurrent
// use (see the package doc).
func (w *Watcher) Process(ev agent.Event) (promoted bool, err error) {
	if err := w.tee(ev); err != nil {
		return false, err
	}
	if ev.Usage != nil {
		if err := w.meter(*ev.Usage); err != nil {
			return false, err
		}
	}
	p, ok := w.rec.Recognize(ev)
	if !ok {
		return false, nil
	}
	if err := w.promote(p); err != nil {
		return false, err
	}
	return true, nil
}

// tee appends the event's raw source line to stream.jsonl, skipping synthesized
// events (nil Raw) and the duplicate Raw carried by the sibling events of one
// fanned-out stream-json line.
func (w *Watcher) tee(ev agent.Event) error {
	if len(ev.Raw) == 0 || bytes.Equal(ev.Raw, w.lastRaw) {
		return nil
	}
	if err := w.sess.AppendStream(ev.Raw); err != nil {
		return fmt.Errorf("watch: tee stream: %w", err)
	}
	w.lastRaw = append(w.lastRaw[:0], ev.Raw...)
	return nil
}

// meter folds a usage snapshot into meter.json. Two figures are folded on
// different rules because the stream reports them on different frames:
//
//   - Cost is folded monotonically. Per-message usage frames report a zero cost
//     and only the turn's terminal result line carries the cumulative dollar
//     figure, so a naive replace would drop cost back to zero between turn ends.
//
//   - The context-window gauge (and its sibling token counts) advances only from
//     a real per-request snapshot — a frame whose ContextTokens is non-zero. The
//     terminal result line's usage is a CUMULATIVE turn total (its cache_read can
//     reach millions over a long session) and the adapter zeroes its ContextTokens
//     for exactly this reason; folding it in would clobber the live ~context fill
//     with a session-wide aggregate (the drvctl-009 "2M context" artifact). So a
//     zero-ContextTokens frame folds cost only and leaves the gauge untouched.
func (w *Watcher) meter(u agent.Usage) error {
	_, err := w.sess.UpdateMeter(func(m *session.Meter) {
		if u.CostUSD > m.Usage.CostUSD {
			m.Usage.CostUSD = u.CostUSD
		}
		if u.ContextTokens > 0 {
			cost := m.Usage.CostUSD
			m.Usage = u
			m.Usage.CostUSD = cost
		}
	})
	if err != nil {
		return fmt.Errorf("watch: meter usage: %w", err)
	}
	return nil
}

// promote appends a recognized semantic event to the attempt's durable log and
// advances the meter's progress-watchdog heartbeat (LastEventAt is defined as
// the time of the most recent semantic event).
func (w *Watcher) promote(p Promotion) error {
	_, err := ticketlog.Append(w.root, w.ticket, w.attempt, event.Event{
		Type:      p.Type,
		Actor:     w.actor,
		Refs:      p.Refs,
		Artefacts: p.Artefacts,
		Body:      p.Body,
	})
	if err != nil {
		return fmt.Errorf("watch: promote %s: %w", p.Type, err)
	}
	now := w.now()
	if _, err := w.sess.UpdateMeter(func(m *session.Meter) { m.LastEventAt = now }); err != nil {
		return fmt.Errorf("watch: heartbeat: %w", err)
	}
	return nil
}
