package completion_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/completion"
	"github.com/Dawil/draiver/internal/project"
	"github.com/Dawil/draiver/internal/ticketlog"
)

func toolCall(id, name string, input map[string]any) agent.Event {
	b, _ := json.Marshal(input)
	return agent.Event{Kind: agent.EventToolCall, Tool: &agent.ToolEvent{ID: id, Name: name, Input: b}}
}

func toolResult(id, result string, isErr bool) agent.Event {
	return agent.Event{Kind: agent.EventToolResult, Tool: &agent.ToolEvent{ID: id, Result: result, IsError: isErr}}
}

func notified() agent.Event {
	return agent.Event{Kind: agent.EventTurnEnd, Turn: "success", Origin: "task-notification"}
}

func feed(t *testing.T, g *completion.Gate, evs ...agent.Event) completion.Outcome {
	t.Helper()
	var out completion.Outcome
	for _, ev := range evs {
		o, err := g.Consider(context.Background(), ev)
		if err != nil {
			t.Fatalf("consider %v: %v", ev.Kind, err)
		}
		out = o
	}
	return out
}

// TestBackgroundTasksDeferNudgeAndHalt is the drv-051 regression: a session that
// launched async subagents and ended its turn "waiting on them" is still working —
// no nudge, no escalation, no halt — until every task reports completion; only then
// does the normal hand-off obligation apply.
func TestBackgroundTasksDeferNudgeAndHalt(t *testing.T) {
	root, ticket, att := newFixture(t)
	rec := &recorder{}
	halted := false
	g, err := completion.New(root, ticket, att, "agent:claude-code", rec.prompt, func() error { halted = true; return nil })
	if err != nil {
		t.Fatal(err)
	}

	// Two async agents (no run_in_background flag — Agent defaults to async) and one
	// background Bash.
	feed(t, g,
		toolCall("a1", "Agent", map[string]any{"description": "draft 1"}),
		toolResult("a1", "Async agent launched successfully.\nagentId: x1", false),
		toolCall("a2", "Agent", map[string]any{"description": "draft 2"}),
		toolResult("a2", "Async agent launched successfully.\nagentId: x2", false),
		toolCall("b1", "Bash", map[string]any{"command": "go test ./...", "run_in_background": true}),
		toolResult("b1", "Command running in background with ID: b1x.", false),
	)
	if n := g.Outstanding(); n != 3 {
		t.Fatalf("outstanding = %d, want 3", n)
	}

	for i := 0; i < 3; i++ {
		if out := feed(t, g, success()); out != completion.Waiting {
			t.Fatalf("turn-end %d with tasks running: out=%v, want Waiting", i, out)
		}
	}
	if out := feed(t, g, notified()); out != completion.Waiting {
		t.Fatalf("first notification (2 left): out=%v, want Waiting", out)
	}
	if out := feed(t, g, notified()); out != completion.Waiting {
		t.Fatalf("second notification (1 left): out=%v, want Waiting", out)
	}
	if len(rec.prompts) != 0 || halted {
		t.Fatalf("gate must not nudge or halt while tasks run: prompts=%v halted=%v", rec.prompts, halted)
	}

	// Last task done: back to the ordinary obligation — the nudge.
	if out := feed(t, g, notified()); out != completion.Nudged {
		t.Fatalf("all tasks done: out=%v, want Nudged", out)
	}
	if halted {
		t.Fatal("must not halt on the first unmet turn after the tasks finished")
	}
}

// TestSyncAndFailedLaunchesAreNotBackground: a synchronous subagent (explicit
// run_in_background:false, or no async marker) and a denied background command
// launch nothing, so the gate nudges as before.
func TestSyncAndFailedLaunchesAreNotBackground(t *testing.T) {
	root, ticket, att := newFixture(t)
	rec := &recorder{}
	g, err := completion.New(root, ticket, att, "agent:claude-code", rec.prompt, nil)
	if err != nil {
		t.Fatal(err)
	}
	feed(t, g,
		toolCall("s1", "Agent", map[string]any{"description": "sync", "run_in_background": false}),
		toolResult("s1", "[Subagent hand-back] done", false),
		toolCall("s2", "Agent", map[string]any{"description": "implicit sync"}),
		toolResult("s2", "[Subagent hand-back] done", false),
		toolCall("d1", "Bash", map[string]any{"command": "sleep 1", "run_in_background": true}),
		toolResult("d1", "Permission denied", true),
	)
	// A subagent's own background launch is not the session's.
	sub := toolCall("n1", "Bash", map[string]any{"command": "x", "run_in_background": true})
	sub.ParentToolID = "s1"
	subRes := toolResult("n1", "Command running in background with ID: n.", false)
	subRes.ParentToolID = "s1"
	feed(t, g, sub, subRes)

	if n := g.Outstanding(); n != 0 {
		t.Fatalf("outstanding = %d, want 0", n)
	}
	if out := feed(t, g, success()); out != completion.Nudged {
		t.Fatalf("out=%v, want Nudged", out)
	}
}

// TestBackgroundCapEscalates: tasks that never report completion do not idle the
// session forever — past the cap, the gate escalates with that reason and halts,
// both from a later turn-end and from the reconcile tick's CheckCap.
func TestBackgroundCapEscalates(t *testing.T) {
	for _, via := range []string{"turn-end", "tick"} {
		t.Run(via, func(t *testing.T) {
			root, ticket, att := newFixture(t)
			rec := &recorder{}
			halted := false
			g, err := completion.New(root, ticket, att, "agent:claude-code", rec.prompt, func() error { halted = true; return nil })
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
			g.SetClock(func() time.Time { return now })
			g.SetBackgroundCap(30 * time.Minute)

			feed(t, g,
				toolCall("b1", "Bash", map[string]any{"command": "hang", "run_in_background": true}),
				toolResult("b1", "Command running in background with ID: b1x.", false),
			)
			if out := feed(t, g, success()); out != completion.Waiting {
				t.Fatalf("out=%v, want Waiting", out)
			}
			if out, _ := g.CheckCap(context.Background()); out != completion.Ignored {
				t.Fatalf("CheckCap before the cap: out=%v, want Ignored", out)
			}

			now = now.Add(31 * time.Minute)
			var out completion.Outcome
			if via == "tick" {
				out, err = g.CheckCap(context.Background())
			} else {
				out, err = g.Consider(context.Background(), success())
			}
			if err != nil || out != completion.Escalated || !halted {
				t.Fatalf("past the cap: out=%v err=%v halted=%v, want Escalated+halt", out, err, halted)
			}
			if len(rec.prompts) != 0 {
				t.Fatalf("no nudge expected on a cap escalation: %v", rec.prompts)
			}
			evs, _ := ticketlog.Read(root, ticket, att)
			last := evs[len(evs)-1]
			if last.Type != "escalation" || !strings.Contains(last.Body, "background") || !strings.Contains(last.Body, "30m") {
				t.Fatalf("last event should be the background-cap escalation, got %s: %s", last.Type, last.Body)
			}
			if st, _ := project.Derive(evs); st != project.NeedsMe {
				t.Fatalf("state = %s, want Needs me", st)
			}
			// Latched: a further check does not stack a second escalation.
			if out, _ := g.CheckCap(context.Background()); out != completion.Ignored {
				t.Fatalf("latched CheckCap: out=%v", out)
			}
		})
	}
}
