package limit_test

import (
	"strings"
	"testing"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/limit"
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

func usageEvent(ctxTokens int) agent.Event {
	return agent.Event{Kind: agent.EventUsage, Usage: &agent.Usage{ContextTokens: ctxTokens}}
}

// TestCrossingThresholdStopsAndEscalates is the headline / Done-when: a usage
// frame over the limit records a durable escalation, halts the session, and the
// board shows Needs-me.
func TestCrossingThresholdStopsAndEscalates(t *testing.T) {
	root, ticket, att := newFixture(t)
	var halted bool
	g := limit.New(root, ticket, att, "agent:claude-code", 150_000, func() error {
		halted = true
		return nil
	})

	// Under the limit: nothing happens.
	if out, err := g.Consider(usageEvent(135_520)); err != nil || out != limit.Ignored {
		t.Fatalf("under-limit frame: out=%v err=%v, want Ignored/nil", out, err)
	}
	if halted {
		t.Fatal("halted under the limit")
	}

	// Crossing the limit: stop + escalate.
	out, err := g.Consider(usageEvent(151_000))
	if err != nil {
		t.Fatalf("Consider: %v", err)
	}
	if out != limit.Stopped {
		t.Fatalf("outcome = %v, want Stopped", out)
	}
	if !halted {
		t.Fatal("session was not halted")
	}

	events, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	st, open := project.Derive(events)
	if st != project.NeedsMe {
		t.Fatalf("state = %v, want NeedsMe (auto-stop should park for a human)", st)
	}
	if len(open) != 1 || open[0].Type != "escalation" {
		t.Fatalf("open escalations = %+v, want one escalation", open)
	}
	if !strings.Contains(open[0].Body, "151,000") || !strings.Contains(open[0].Body, "150,000") {
		t.Errorf("escalation body missing the numbers: %q", open[0].Body)
	}
}

// TestFiresAtMostOnce: once stopped, later over-limit frames (which arrive before
// the reap lands) must not stack duplicate escalations.
func TestFiresAtMostOnce(t *testing.T) {
	root, ticket, att := newFixture(t)
	g := limit.New(root, ticket, att, "agent:claude-code", 100_000, func() error { return nil })

	if out, _ := g.Consider(usageEvent(120_000)); out != limit.Stopped {
		t.Fatalf("first crossing out=%v, want Stopped", out)
	}
	if out, _ := g.Consider(usageEvent(130_000)); out != limit.Ignored {
		t.Fatalf("second crossing out=%v, want Ignored (latched)", out)
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

// TestDisabledThresholdNeverFires: a threshold <= 0 turns the gate off, even for
// an enormous context reading.
func TestDisabledThresholdNeverFires(t *testing.T) {
	root, ticket, att := newFixture(t)
	var halted bool
	g := limit.New(root, ticket, att, "agent:claude-code", 0, func() error {
		halted = true
		return nil
	})
	if out, err := g.Consider(usageEvent(2_000_000)); err != nil || out != limit.Ignored {
		t.Fatalf("disabled gate: out=%v err=%v, want Ignored/nil", out, err)
	}
	if halted {
		t.Fatal("disabled gate halted the session")
	}
}

// TestNonUsageEventsIgnored: an event without a usage frame is a no-op.
func TestNonUsageEventsIgnored(t *testing.T) {
	root, ticket, att := newFixture(t)
	g := limit.New(root, ticket, att, "agent:claude-code", 100, func() error { return nil })
	if out, err := g.Consider(agent.Event{Kind: agent.EventAssistant, Text: "hi"}); err != nil || out != limit.Ignored {
		t.Fatalf("assistant event: out=%v err=%v, want Ignored/nil", out, err)
	}
}
