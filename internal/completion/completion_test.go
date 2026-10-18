package completion_test

import (
	"context"
	"os"
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

// makeParkable turns the fixture ticket into a `wants:` coordinator with a child
// still in flight, so wake.Pending reports a source — the condition under which
// the gate accepts a `park` as a hand-off and suspends (rather than escalates) an
// unmet turn. It writes the parent's spec frontmatter and creates a not-Done child
// attempt.
func makeParkable(t *testing.T, root store.Root, ticket string) {
	t.Helper()
	fm := "---\nid: " + ticket + "\nwants: [CHILD-1]\n---\n\nbody\n"
	if err := os.WriteFile(root.SpecPath(ticket), []byte(fm), 0o644); err != nil {
		t.Fatalf("write parent spec: %v", err)
	}
	if err := root.EnsureTicketDir("CHILD-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.Create(root, "CHILD-1", attempt.New{Tool: "claude-code", Model: "opus-4.8", Repo: "/repo", Actor: "human:x"}); err != nil {
		t.Fatalf("create child attempt: %v", err)
	}
}

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

// TestParkableSuspendsInsteadOfEscalating is drvctl-052 AC2: a parkable attempt (a
// `wants:` coordinator with a child still in flight — e.g. one that delegated via
// `resolve --by` and then stopped) that ends a success turn without a hand-off is
// nudged once, then SUSPENDED (Parked), never escalated. No escalation lands on its
// own log, its state stays Running (the read-time projection drops it to Pending
// once reaped), and the session is not halted at the gate.
func TestParkableSuspendsInsteadOfEscalating(t *testing.T) {
	ctx := context.Background()
	root, ticket, att := newFixture(t)
	makeParkable(t, root, ticket)
	rec := &recorder{}
	var halted bool
	g, err := completion.New(root, ticket, att, "agent:claude-code", rec.prompt, func() error { halted = true; return nil })
	if err != nil {
		t.Fatal(err)
	}

	// First unmet success → nudge; the parkable nudge offers `park` as the stop.
	if out, err := g.Consider(ctx, success()); err != nil || out != completion.Nudged {
		t.Fatalf("first success: out=%v err=%v, want Nudged/nil", out, err)
	}
	if len(rec.prompts) != 1 || !strings.Contains(rec.prompts[0], "park") {
		t.Fatalf("parkable nudge must offer park: %v", rec.prompts)
	}

	// Second unmet success → Parked, not Escalated. No halt, no escalation on disk.
	if out, err := g.Consider(ctx, success()); err != nil || out != completion.Parked {
		t.Fatalf("second success: out=%v err=%v, want Parked/nil (suspend, not escalate)", out, err)
	}
	if halted {
		t.Fatal("a park suspension must not reap the session at the gate — the coordinator gate retires it")
	}
	events, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	st, open := project.Derive(events)
	if st == project.NeedsMe || len(open) != 0 {
		t.Fatalf("parkable suspend must not manufacture an escalation: state=%v open=%+v", st, open)
	}

	// Latched: a later success frame is a no-op (no second decision stacked).
	if out, _ := g.Consider(ctx, success()); out != completion.Ignored {
		t.Fatalf("third success out=%v, want Ignored (latched after Parked)", out)
	}
}

// TestParkCountsAsHandoffWhenParkable is drvctl-052 AC1 on the gate side: a durable
// `park` newer than the baseline meets the hand-off obligation when the attempt is
// parkable, so the success turn is Ignored — no nudge, no escalation.
func TestParkCountsAsHandoffWhenParkable(t *testing.T) {
	ctx := context.Background()
	root, ticket, att := newFixture(t)
	makeParkable(t, root, ticket)
	rec := &recorder{}
	g, err := completion.New(root, ticket, att, "agent:claude-code", rec.prompt, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "park", Actor: "agent:x", Body: "delegated; nothing needs me"}); err != nil {
		t.Fatal(err)
	}
	if out, err := g.Consider(ctx, success()); err != nil || out != completion.Ignored {
		t.Fatalf("success after a parkable park: out=%v err=%v, want Ignored/nil", out, err)
	}
	if len(rec.prompts) != 0 {
		t.Fatalf("a legitimate park is a hand-off — must not nudge: %v", rec.prompts)
	}
}

// TestAutoParkOnLiveAsyncJob is drvctl-055's safety net (AC #5): a leaf worker that
// launched a detached test and ended its turn WITHOUT the `&& draiver park` has a
// live async job but no explicit hand-off. The gate must not escalate (it is a
// test-wait, not a human question); instead it auto-authors the park the agent
// omitted, so the async-dormancy reconciler — which keys on a park after the launch
// — reaps the idle session to Pending rather than letting it linger.
func TestAutoParkOnLiveAsyncJob(t *testing.T) {
	ctx := context.Background()
	root, ticket, att := newFixture(t) // a leaf — its only wake source is the async job
	// `draiver test --detach` wrote this launch marker; the agent then stopped without
	// parking, so there is no park on the log yet.
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "async-job", Actor: "agent:x", Rung: "unit", Body: "launched detached test"}); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	var halted bool
	g, err := completion.New(root, ticket, att, "agent:claude-code", rec.prompt, func() error { halted = true; return nil })
	if err != nil {
		t.Fatal(err)
	}

	// First unmet success → nudge (park offered, since the async job makes it parkable).
	if out, err := g.Consider(ctx, success()); err != nil || out != completion.Nudged {
		t.Fatalf("first success: out=%v err=%v, want Nudged/nil", out, err)
	}
	// Second unmet success → Parked, and the gate authors the park the agent omitted.
	if out, err := g.Consider(ctx, success()); err != nil || out != completion.Parked {
		t.Fatalf("second success: out=%v err=%v, want Parked/nil", out, err)
	}
	if halted {
		t.Fatal("an auto-park must not reap the session at the gate — the async reconciler retires it")
	}

	events, err := ticketlog.Read(root, ticket, att)
	if err != nil {
		t.Fatal(err)
	}
	var parks int
	for _, e := range events {
		if e.Type == "park" {
			parks++
		}
	}
	if parks != 1 {
		t.Fatalf("the gate must author exactly one park on behalf of the agent, got %d", parks)
	}
	// And it must not have manufactured a human escalation.
	st, open := project.Derive(events)
	if st == project.NeedsMe || len(open) != 0 {
		t.Fatalf("auto-park must not escalate: state=%v open=%+v", st, open)
	}
}

// TestParkIsNotHandoffWithoutWakeSource is the guard on the other side: a `park`
// filed by an attempt with NO wake source (a leaf) would sleep forever, so it is
// not a legitimate hand-off. The gate ignores it and still nudges the unmet turn —
// so a stray park cannot silently strand a leaf in Running + enabled.
func TestParkIsNotHandoffWithoutWakeSource(t *testing.T) {
	ctx := context.Background()
	root, ticket, att := newFixture(t) // a leaf: no wants: edges, not parkable
	rec := &recorder{}
	g, err := completion.New(root, ticket, att, "agent:claude-code", rec.prompt, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ticketlog.Append(root, ticket, att, event.Event{Type: "park", Actor: "agent:x", Body: "stray park"}); err != nil {
		t.Fatal(err)
	}
	if out, err := g.Consider(ctx, success()); err != nil || out != completion.Nudged {
		t.Fatalf("park with no wake source must not satisfy: out=%v err=%v, want Nudged/nil", out, err)
	}
}
