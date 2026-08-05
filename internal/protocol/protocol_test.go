package protocol_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/attempt"
	"github.com/Dawil/draiver/internal/event"
	"github.com/Dawil/draiver/internal/protocol"
	"github.com/Dawil/draiver/internal/store"
	"github.com/Dawil/draiver/internal/ticketlog"
)

// fakeDecider records the decisions the gate hands back to the agent, standing in
// for the adapter's agent.Permissioner without a live session.
type fakeDecider struct {
	mu    sync.Mutex
	calls []decideCall
	err   error // returned by Decide when set
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

// newFixture builds a real ticket+attempt on disk so ticketlog.Append/Read and
// brief.Build run against genuine state.
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

func permEvent(id, tool string) agent.Event {
	return agent.Event{
		Kind:       agent.EventPermission,
		Permission: &agent.PermissionRequest{ID: id, Tool: tool},
	}
}

func logEvent(t *testing.T, root store.Root, ticket, att, typ, body string) {
	t.Helper()
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: typ, Actor: "agent:claude-code", Body: body}); err != nil {
		t.Fatal(err)
	}
}

func mustGate(t *testing.T, root store.Root, ticket, att string, p protocol.Policy, dec protocol.Decider) *protocol.Gate {
	t.Helper()
	g, err := protocol.New(root, ticket, att, p, dec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

// TestEditWithheldUntilLogged is the headline / Done-when: an agent cannot land a
// code edit without the corresponding log event existing. A Justified tool is
// withheld with guidance until a decision is on disk, then the same call clears.
func TestEditWithheldUntilLogged(t *testing.T) {
	root, ticket, att := newFixture(t)
	dec := &fakeDecider{}
	g := mustGate(t, root, ticket, att, protocol.Edits(), dec)

	// 1. First edit, no rationale logged yet → withheld with a deny + guidance.
	out, err := g.Consider(context.Background(), permEvent("req-1", "Edit"))
	if err != nil {
		t.Fatalf("Consider: %v", err)
	}
	if out != protocol.Withheld {
		t.Fatalf("outcome = %v, want Withheld", out)
	}
	if dec.count() != 1 {
		t.Fatalf("decider calls = %d, want 1 (the withhold deny)", dec.count())
	}
	call := dec.last()
	if call.id != "req-1" || call.d.Allow {
		t.Fatalf("decision = %+v, want deny of req-1", call)
	}
	if !strings.Contains(call.d.Message, "draiver log") || !strings.Contains(call.d.Message, "Edit") {
		t.Errorf("withhold message lacks actionable guidance:\n%s", call.d.Message)
	}

	// 2. The agent logs the rationale that justifies the edit.
	logEvent(t, root, ticket, att, "decision", "Switch the timeout to a context deadline.")

	// 3. Retry the same edit → cleared, and the gate does NOT answer (the
	//    permission gate is the approver; veto-only means no new decider call).
	out, err = g.Consider(context.Background(), permEvent("req-2", "Edit"))
	if err != nil {
		t.Fatalf("Consider after log: %v", err)
	}
	if out != protocol.Cleared {
		t.Fatalf("outcome after log = %v, want Cleared", out)
	}
	if dec.count() != 1 {
		t.Fatalf("decider calls = %d after clear, want still 1 (veto-only never allows)", dec.count())
	}
}

// TestFreeToolsCleared: read-only tools and Bash run without a rationale and
// without the gate answering — Bash stays Free so the agent can log through it.
func TestFreeToolsCleared(t *testing.T) {
	root, ticket, att := newFixture(t)
	dec := &fakeDecider{}
	g := mustGate(t, root, ticket, att, protocol.Edits(), dec)

	for _, tool := range []string{"Read", "Grep", "Bash"} {
		out, err := g.Consider(context.Background(), permEvent("req", tool))
		if err != nil {
			t.Fatalf("Consider(%s): %v", tool, err)
		}
		if out != protocol.Cleared {
			t.Fatalf("Consider(%s) = %v, want Cleared", tool, out)
		}
	}
	if dec.count() != 0 {
		t.Fatalf("decider called %d times for Free tools; the gate must not answer them", dec.count())
	}
}

// TestGotchaAlsoJustifies: a gotcha is a valid rationale, not only a decision.
func TestGotchaAlsoJustifies(t *testing.T) {
	root, ticket, att := newFixture(t)
	dec := &fakeDecider{}
	g := mustGate(t, root, ticket, att, protocol.Edits(), dec)

	logEvent(t, root, ticket, att, "gotcha", "The old client swallows 5xx; must special-case it.")

	out, err := g.Consider(context.Background(), permEvent("req-1", "Write"))
	if err != nil {
		t.Fatalf("Consider: %v", err)
	}
	if out != protocol.Cleared {
		t.Fatalf("outcome = %v, want Cleared (a gotcha justifies)", out)
	}
	if dec.count() != 0 {
		t.Fatalf("decider calls = %d, want 0", dec.count())
	}
}

// TestNoteDoesNotJustify: a plain note is weaker context and does not unlock a
// withheld edit — only a decision or gotcha does.
func TestNoteDoesNotJustify(t *testing.T) {
	root, ticket, att := newFixture(t)
	dec := &fakeDecider{}
	g := mustGate(t, root, ticket, att, protocol.Edits(), dec)

	logEvent(t, root, ticket, att, "note", "Looking into the timeout.")

	out, err := g.Consider(context.Background(), permEvent("req-1", "Edit"))
	if err != nil {
		t.Fatalf("Consider: %v", err)
	}
	if out != protocol.Withheld {
		t.Fatalf("outcome = %v, want Withheld (a note is not a rationale)", out)
	}
}

// TestBaselineIsSessionStart: a rationale logged BEFORE the gate was constructed
// (an earlier session) does not clear a withheld edit — the justification must be
// newer than the session baseline, so each driven session justifies its own work.
func TestBaselineIsSessionStart(t *testing.T) {
	root, ticket, att := newFixture(t)
	// A decision from a prior session is already on disk before the gate is built.
	logEvent(t, root, ticket, att, "decision", "Prior session's rationale.")

	dec := &fakeDecider{}
	g := mustGate(t, root, ticket, att, protocol.Edits(), dec) // baseline snapshots the prior decision

	out, err := g.Consider(context.Background(), permEvent("req-1", "Edit"))
	if err != nil {
		t.Fatalf("Consider: %v", err)
	}
	if out != protocol.Withheld {
		t.Fatalf("outcome = %v, want Withheld (stale rationale must not count)", out)
	}

	// A fresh in-session rationale clears it.
	logEvent(t, root, ticket, att, "decision", "This session's rationale.")
	out, err = g.Consider(context.Background(), permEvent("req-2", "Edit"))
	if err != nil {
		t.Fatalf("Consider after fresh log: %v", err)
	}
	if out != protocol.Cleared {
		t.Fatalf("outcome = %v, want Cleared after a fresh rationale", out)
	}
}

// TestNonPermissionEventIgnored: the gate is a no-op for the rest of the stream.
func TestNonPermissionEventIgnored(t *testing.T) {
	root, ticket, att := newFixture(t)
	dec := &fakeDecider{}
	g := mustGate(t, root, ticket, att, protocol.Edits(), dec)

	for _, ev := range []agent.Event{
		{Kind: agent.EventAssistant, Text: "hi"},
		{Kind: agent.EventToolCall, Tool: &agent.ToolEvent{Name: "Edit"}},
		{Kind: agent.EventPermission}, // nil Permission — malformed, ignore
	} {
		out, err := g.Consider(context.Background(), ev)
		if err != nil {
			t.Fatalf("Consider(%v): %v", ev.Kind, err)
		}
		if out != protocol.Ignored {
			t.Fatalf("Consider(%v) = %v, want Ignored", ev.Kind, out)
		}
	}
	if dec.count() != 0 {
		t.Fatalf("decider was called %d times for ignored events", dec.count())
	}
}

// TestNilDeciderRejected: a Gate cannot be built without a way to answer the agent.
func TestNilDeciderRejected(t *testing.T) {
	root, ticket, att := newFixture(t)
	if _, err := protocol.New(root, ticket, att, protocol.Edits(), nil); err == nil {
		t.Fatal("New with a nil decider should error")
	}
}

// fakePrompter captures the text InjectBrief prompts into the session.
type fakePrompter struct {
	got string
	err error
}

func (f *fakePrompter) Prompt(ctx context.Context, text string) error {
	f.got = text
	return f.err
}

// TestInjectBrief: the cold-start half prompts the built brief (spec + log) into
// the session, framed so the agent treats it as authoritative context.
func TestInjectBrief(t *testing.T) {
	root, ticket, att := newFixture(t)
	os.WriteFile(root.SpecPath(ticket), []byte("---\nid: PROJ-1\ntitle: Wire auth\n---\n\n# Wire auth\n\nUse OAuth."), 0o644)
	logEvent(t, root, ticket, att, "decision", "chose cobra over urfave")

	p := &fakePrompter{}
	if err := protocol.InjectBrief(context.Background(), root, ticket, att, p); err != nil {
		t.Fatalf("InjectBrief: %v", err)
	}

	for _, want := range []string{
		"cold-starting a draiver attempt", // the supervisor preamble
		"Work the protocol",
		"BRIEF PROJ-1",            // the built brief
		"Use OAuth.",              // spec body
		"chose cobra over urfave", // a logged decision
	} {
		if !strings.Contains(p.got, want) {
			t.Errorf("injected brief missing %q\n---\n%s", want, p.got)
		}
	}
}

// TestInjectBriefBuildError: a brief that cannot be built (unknown attempt) is a
// surfaced error, and nothing is prompted.
func TestInjectBriefBuildError(t *testing.T) {
	root, ticket, _ := newFixture(t)
	p := &fakePrompter{}
	if err := protocol.InjectBrief(context.Background(), root, ticket, "9999", p); err == nil {
		t.Fatal("InjectBrief for a missing attempt should error")
	}
	if p.got != "" {
		t.Fatalf("nothing should be prompted on a build error; got %q", p.got)
	}
}
