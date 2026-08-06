package completion_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/completion"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// newFixture builds a real ticket+attempt on disk so ticketlog.Append and
// project.Derive run against genuine state.
func newFixture(t *testing.T) (store.Root, string, string) {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}
	ticket := "PROJ-1"
	if err := root.EnsureTicketDir(ticket); err != nil {
		t.Fatal(err)
	}
	m, err := attempt.Create(root, ticket, attempt.New{Tool: "claude-code", Model: "opus-4.8", Repo: "/repo", Actor: "human:x"})
	if err != nil {
		t.Fatal(err)
	}
	return root, ticket, m.ID
}

func success() agent.Event { return agent.Event{Kind: agent.EventTurnEnd, Turn: "success"} }

// recorder captures the nudge prompts a gate sends.
type recorder struct{ prompts []string }

func (r *recorder) prompt(_ context.Context, text string) error {
	r.prompts = append(r.prompts, text)
	return nil
}

// TestNudgeThenEscalate is the headline: a first unmet success turn nudges; a
// second escalates to a human and halts, parking the attempt at Needs-me.
func TestNudgeThenEscalate(t *testing.T) {
	ctx := context.Background()
	root, ticket, att := newFixture(t)
	rec := &recorder{}
	var halted bool
	g, err := completion.New(root, ticket, att, "agent:claude-code", rec.prompt, func() error { halted = true; return nil })
	if err != nil {
		t.Fatal(err)
	}

	// First unmet success → nudge, not escalate, not halt.
	if out, err := g.Consider(ctx, success()); err != nil || out != completion.Nudged {
		t.Fatalf("first success: out=%v err=%v, want Nudged/nil", out, err)
	}
	if len(rec.prompts) != 1 || !strings.Contains(rec.prompts[0], "review") || !strings.Contains(rec.prompts[0], "escalate") {
		t.Fatalf("nudge prompt missing guidance: %v", rec.prompts)
	}
	if halted {
		t.Fatal("a nudge must not halt")
	}

	// Second unmet success → escalate + halt.
	if out, err := g.Consider(ctx, success()); err != nil || out != completion.Escalated {
		t.Fatalf("second success: out=%v err=%v, want Escalated/nil", out, err)
	}
	if !halted {
		t.Fatal("escalation must halt the session")
	}

	events, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	st, open := project.Derive(events)
	if st != project.NeedsMe {
		t.Fatalf("state = %v, want NeedsMe after stall escalation", st)
	}
	if len(open) != 1 || open[0].Type != "escalation" {
		t.Fatalf("open escalations = %+v, want one escalation", open)
	}
}

// TestHandoffSatisfies: a review newer than the baseline meets the obligation, so a
// success turn is Ignored — no nudge, no escalation.
func TestHandoffSatisfies(t *testing.T) {
	ctx := context.Background()
	root, ticket, att := newFixture(t)
	rec := &recorder{}
	g, err := completion.New(root, ticket, att, "agent:claude-code", rec.prompt, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "review", Actor: "agent:x", Body: "done"}); err != nil {
		t.Fatal(err)
	}
	if out, err := g.Consider(ctx, success()); err != nil || out != completion.Ignored {
		t.Fatalf("success after review: out=%v err=%v, want Ignored/nil", out, err)
	}
	if len(rec.prompts) != 0 {
		t.Fatalf("must not nudge once handed off: %v", rec.prompts)
	}
}

// TestStaleHandoffDoesNotCount: a review/escalation from *before* the session
// baseline is not this session's hand-off, so the guard still fires.
func TestStaleHandoffDoesNotCount(t *testing.T) {
	ctx := context.Background()
	root, ticket, att := newFixture(t)
	// A review is already on record before the gate is constructed — a prior
	// session's hand-off. The baseline snapshot must exclude it.
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "review", Actor: "agent:x", Body: "old"}); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	g, err := completion.New(root, ticket, att, "agent:claude-code", rec.prompt, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if out, err := g.Consider(ctx, success()); err != nil || out != completion.Nudged {
		t.Fatalf("stale review must not satisfy: out=%v err=%v, want Nudged/nil", out, err)
	}
}

// TestNonSuccessTurnsIgnored: an error/interrupted turn is not a false "done", and
// a non-turn-end event is a no-op.
func TestNonSuccessTurnsIgnored(t *testing.T) {
	ctx := context.Background()
	root, ticket, att := newFixture(t)
	rec := &recorder{}
	g, err := completion.New(root, ticket, att, "agent:claude-code", rec.prompt, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []agent.Event{
		{Kind: agent.EventTurnEnd, Turn: "error"},
		{Kind: agent.EventTurnEnd, Turn: "interrupted"},
		{Kind: agent.EventAssistant, Text: "thinking"},
	} {
		if out, err := g.Consider(ctx, ev); err != nil || out != completion.Ignored {
			t.Fatalf("%+v: out=%v err=%v, want Ignored/nil", ev, out, err)
		}
	}
	if len(rec.prompts) != 0 {
		t.Fatalf("no nudge for non-success turns: %v", rec.prompts)
	}
}

// TestLatchesAfterEscalating: once escalated, later success frames (arriving before
// the reap lands) must not stack duplicate escalations.
func TestLatchesAfterEscalating(t *testing.T) {
	ctx := context.Background()
	root, ticket, att := newFixture(t)
	// nudge nil → escalate a step sooner (first unmet success escalates).
	g, err := completion.New(root, ticket, att, "agent:claude-code", nil, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := g.Consider(ctx, success()); out != completion.Escalated {
		t.Fatalf("with no nudger, first success should Escalate, got %v", out)
	}
	if out, _ := g.Consider(ctx, success()); out != completion.Ignored {
		t.Fatalf("second success out=%v, want Ignored (latched)", out)
	}
	events, _ := ticketlog.Read(root, ticket, att)
	n := 0
	for _, e := range events {
		if e.Type == "escalation" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("escalations recorded = %d, want exactly 1", n)
	}
}
