package gate_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/gate"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// fakeDecider records the decisions the gate hands back to the agent, standing
// in for the adapter's agent.Permissioner without a live session.
type fakeDecider struct {
	mu       sync.Mutex
	calls    []decideCall
	err      error // returned by Decide when set
}

type decideCall struct {
	id string
	d  agent.Decision
}

func (f *fakeDecider) Decide(ctx context.Context, id string, d agent.Decision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, decideCall{id: id, d: d})
	return f.err
}

func (f *fakeDecider) last() decideCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func (f *fakeDecider) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

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

func permEvent(id, tool string, input map[string]any) agent.Event {
	raw, _ := json.Marshal(input)
	return agent.Event{
		Kind:       agent.EventPermission,
		Permission: &agent.PermissionRequest{ID: id, Tool: tool, Input: raw},
	}
}

// TestGatedToolEscalatesAndHalts is the headline / Done-when: a gated tool call
// produces a durable escalation, halts the session, and the board shows Needs-me.
func TestGatedToolEscalatesAndHalts(t *testing.T) {
	root, ticket, att := newFixture(t)
	dec := &fakeDecider{}
	var halted bool
	g := gate.New(root, ticket, att, "agent:claude-code", gate.ReadOnly(), dec, func() error {
		halted = true
		return nil
	})

	out, err := g.Consider(context.Background(), permEvent("req-7", "Bash", map[string]any{"command": "rm -rf /"}))
	if err != nil {
		t.Fatalf("Consider: %v", err)
	}
	if out != gate.Escalated {
		t.Fatalf("outcome = %v, want Escalated", out)
	}

	// 1. A durable escalation reached the log.
	events, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	var sawEscalation bool
	var escBody string
	for _, e := range events {
		if e.Type == "escalation" {
			sawEscalation = true
			escBody = e.Body
			if e.Actor != "agent:claude-code" {
				t.Errorf("escalation actor = %q, want agent:claude-code", e.Actor)
			}
		}
	}
	if !sawEscalation {
		t.Fatal("no escalation event was appended to the log")
	}
	if !strings.Contains(escBody, "Bash") || !strings.Contains(escBody, "rm -rf /") {
		t.Errorf("escalation body missing tool/input detail:\n%s", escBody)
	}

	// 2. The session was halted.
	if !halted {
		t.Fatal("the session was not halted after the escalation")
	}

	// 3. The pending call was denied so the agent is not left waiting.
	if dec.count() != 1 {
		t.Fatalf("decider calls = %d, want 1 (the deny)", dec.count())
	}
	if last := dec.last(); last.id != "req-7" || last.d.Allow {
		t.Fatalf("last decision = %+v, want deny of req-7", last)
	}

	// 4. The board derives Needs-me from the open escalation.
	a, err := project.LoadAttempt(root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != project.NeedsMe {
		t.Fatalf("control state = %q, want %q", a.State, project.NeedsMe)
	}
	if len(a.OpenEscalations) != 1 {
		t.Fatalf("open escalations = %d, want 1", len(a.OpenEscalations))
	}
}

// TestAllowedToolProceeds: a read-only tool is auto-approved back to the agent,
// with no escalation, no halt, and the original input echoed.
func TestAllowedToolProceeds(t *testing.T) {
	root, ticket, att := newFixture(t)
	dec := &fakeDecider{}
	halted := false
	g := gate.New(root, ticket, att, "agent:claude-code", gate.ReadOnly(), dec, func() error {
		halted = true
		return nil
	})

	in := map[string]any{"file_path": "/etc/hosts"}
	out, err := g.Consider(context.Background(), permEvent("req-1", "Read", in))
	if err != nil {
		t.Fatalf("Consider: %v", err)
	}
	if out != gate.Allowed {
		t.Fatalf("outcome = %v, want Allowed", out)
	}
	if halted {
		t.Fatal("an allowed tool must not halt the session")
	}
	if dec.count() != 1 {
		t.Fatalf("decider calls = %d, want 1 (the allow)", dec.count())
	}
	call := dec.last()
	if call.id != "req-1" || !call.d.Allow {
		t.Fatalf("decision = %+v, want allow of req-1", call)
	}
	// The gate echoes the request's input unchanged.
	var got map[string]any
	if err := json.Unmarshal(call.d.Input, &got); err != nil {
		t.Fatalf("echoed input not JSON: %v", err)
	}
	if got["file_path"] != "/etc/hosts" {
		t.Fatalf("echoed input = %v, want the original", got)
	}

	// No escalation, so the attempt stays Running.
	a, err := project.LoadAttempt(root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != project.Running {
		t.Fatalf("control state = %q, want Running", a.State)
	}
}

// TestNonPermissionEventIgnored: the gate is a no-op for the rest of the stream.
func TestNonPermissionEventIgnored(t *testing.T) {
	root, ticket, att := newFixture(t)
	dec := &fakeDecider{}
	g := gate.New(root, ticket, att, "agent:x", gate.ReadOnly(), dec, func() error {
		t.Fatal("halt must not fire for a non-permission event")
		return nil
	})

	for _, ev := range []agent.Event{
		{Kind: agent.EventAssistant, Text: "hi"},
		{Kind: agent.EventToolCall, Tool: &agent.ToolEvent{Name: "Bash"}},
		{Kind: agent.EventPermission}, // nil Permission — malformed, ignore
	} {
		out, err := g.Consider(context.Background(), ev)
		if err != nil {
			t.Fatalf("Consider(%v): %v", ev.Kind, err)
		}
		if out != gate.Ignored {
			t.Fatalf("Consider(%v) = %v, want Ignored", ev.Kind, out)
		}
	}
	if dec.count() != 0 {
		t.Fatalf("decider was called %d times for ignored events", dec.count())
	}
}

// TestEscalationRecordedBeforeHaltFailure: if the reap fails, the escalation is
// still durable and the outcome is still Escalated (the attempt is blocked).
func TestEscalationRecordedBeforeHaltFailure(t *testing.T) {
	root, ticket, att := newFixture(t)
	dec := &fakeDecider{}
	g := gate.New(root, ticket, att, "agent:x", gate.ReadOnly(), dec, func() error {
		return context.DeadlineExceeded
	})

	out, err := g.Consider(context.Background(), permEvent("req-9", "Write", map[string]any{"file_path": "x"}))
	if out != gate.Escalated {
		t.Fatalf("outcome = %v, want Escalated even on halt failure", out)
	}
	if err == nil {
		t.Fatal("a halt failure should surface an error")
	}
	a, err2 := project.LoadAttempt(root, ticket, att)
	if err2 != nil {
		t.Fatal(err2)
	}
	if a.State != project.NeedsMe {
		t.Fatalf("state = %q, want Needs-me despite halt failure", a.State)
	}
}

// TestNilHaltStillEscalates: halt is optional; without it the gate still records
// the escalation and denies the call.
func TestNilHaltStillEscalates(t *testing.T) {
	root, ticket, att := newFixture(t)
	dec := &fakeDecider{}
	g := gate.New(root, ticket, att, "agent:x", gate.ReadOnly(), dec, nil)

	out, err := g.Consider(context.Background(), permEvent("req-2", "Bash", nil))
	if err != nil {
		t.Fatalf("Consider: %v", err)
	}
	if out != gate.Escalated {
		t.Fatalf("outcome = %v, want Escalated", out)
	}
	a, _ := project.LoadAttempt(root, ticket, att)
	if a.State != project.NeedsMe {
		t.Fatalf("state = %q, want Needs-me", a.State)
	}
}
