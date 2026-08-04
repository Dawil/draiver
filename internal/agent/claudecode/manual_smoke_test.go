//go:build manual

package claudecode_test

// Manual end-to-end smoke against the real `claude` binary. Excluded from the
// default build (tag `manual`); it spends tokens and needs auth. Run with:
//
//	go test -tags manual -run TestManualSmoke -v ./internal/agent/claudecode
//
// It proves the Done-when: spawn against a workdir, prompt, read turns as
// normalized events, then interrupt/kill.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
	"github.com/Dawil/draiver/internal/agent/claudecode"
)

func TestManualSmoke(t *testing.T) {
	wd, err := os.MkdirTemp("", "cc-smoke-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(wd)

	a := claudecode.New()
	ctx := context.Background()
	id, err := a.Spawn(ctx, agent.SessionSpec{WorkDir: wd, Model: "claude-haiku-4-5-20251001"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Logf("session id: %s", id)

	if err := a.Prompt(ctx, "Reply with exactly the word OK and nothing else."); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	var sawAssistant, sawTurnEnd bool
	deadline := time.After(90 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-a.Stream():
			if !ok {
				break loop
			}
			switch ev.Kind {
			case agent.EventSystem:
				t.Logf("system: session=%s", ev.SessionID)
			case agent.EventAssistant:
				t.Logf("assistant (thinking=%v): %q", ev.Thinking, ev.Text)
				if !ev.Thinking {
					sawAssistant = true
				}
			case agent.EventUsage:
				t.Logf("usage: ctx=%d out=%d cost=$%.4f", ev.Usage.ContextTokens, ev.Usage.OutputTokens, ev.Usage.CostUSD)
			case agent.EventTurnEnd:
				t.Logf("turn_end: status=%s result=%q", ev.Turn, ev.Result)
				sawTurnEnd = true
				break loop
			case agent.EventError:
				t.Fatalf("stream error: %s", ev.Err)
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}

	if !sawAssistant || !sawTurnEnd {
		t.Fatalf("incomplete turn: assistant=%v turnEnd=%v", sawAssistant, sawTurnEnd)
	}
	if err := a.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	t.Log("killed cleanly")
}
