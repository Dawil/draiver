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
	"path/filepath"
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

// TestManualSmokePermissionGate is the live end-to-end for drvctl-013: it proves,
// against the real `claude` binary, that the permission callback actually
// activates (so the gate is fed) and that acceptEdits gives the intended split —
// edits inside the workdir (the attempt's sandbox) flow without a human, while a
// write outside it is routed back to the client as an EventPermission the gate
// answers. Before the --permission-prompt-tool fix, the in-workdir write was
// auto-denied ("…you haven't granted it yet") and no permission event ever fired.
//
//	go test -tags manual -run TestManualSmokePermissionGate -v ./internal/agent/claudecode
func TestManualSmokePermissionGate(t *testing.T) {
	t.Run("in-workdir write flows without approval", func(t *testing.T) {
		wd, err := os.MkdirTemp("", "cc-gate-in-*")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(wd)

		a := claudecode.New()
		ctx := context.Background()
		if _, err := a.Spawn(ctx, agent.SessionSpec{
			WorkDir:        wd,
			Model:          "claude-haiku-4-5-20251001",
			PermissionMode: "acceptEdits", // the ctl default
		}); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		defer a.Kill()

		target := filepath.Join(wd, "probe.txt")
		if err := a.Prompt(ctx, "Use the Write tool to create the file probe.txt in the current directory with the exact contents: hello. Do it now."); err != nil {
			t.Fatalf("Prompt: %v", err)
		}

		var sawPermission bool
		drainToTurnEnd(t, a, func(ev agent.Event) {
			if ev.Kind == agent.EventPermission {
				// In acceptEdits this should NOT happen for an in-workdir edit; answer
				// allow so a regression surfaces as sawPermission rather than a hang.
				sawPermission = true
				_ = a.Decide(ctx, ev.Permission.ID, agent.Decision{Allow: true, Input: ev.Permission.Input})
			}
		})

		if sawPermission {
			t.Errorf("in-workdir edit was routed to the gate; expected the classifier to auto-approve it")
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("agent did not create %s: %v", target, err)
		}
		t.Logf("in-workdir write succeeded without approval: %s", target)
	})

	t.Run("out-of-workdir write hits the gate seam", func(t *testing.T) {
		wd, err := os.MkdirTemp("", "cc-gate-out-*")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(wd)
		outside := filepath.Join(t.TempDir(), "outside.txt") // a path NOT under wd

		a := claudecode.New()
		ctx := context.Background()
		if _, err := a.Spawn(ctx, agent.SessionSpec{
			WorkDir:        wd,
			Model:          "claude-haiku-4-5-20251001",
			PermissionMode: "acceptEdits",
		}); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		defer a.Kill()

		if err := a.Prompt(ctx, "Use the Write tool to create the file "+outside+" with the contents: hi. Do it now."); err != nil {
			t.Fatalf("Prompt: %v", err)
		}

		var gated bool
		drainToTurnEnd(t, a, func(ev agent.Event) {
			if ev.Kind == agent.EventPermission {
				gated = true
				t.Logf("gate seam fired for tool %q input=%s", ev.Permission.Tool, string(ev.Permission.Input))
				// Deny it, as the gate would on an escalation.
				_ = a.Decide(ctx, ev.Permission.ID, agent.Decision{Allow: false, Message: "smoke: denied by gate"})
			}
		})

		if !gated {
			t.Errorf("out-of-workdir write was not routed to the gate; the gate seam is not live")
		}
		if _, err := os.Stat(outside); err == nil {
			t.Errorf("denied write nonetheless created %s", outside)
		}
	})
}

// drainToTurnEnd reads the stream until a turn ends (or times out), invoking
// onEvent for each event so a test can answer permission requests inline.
func drainToTurnEnd(t *testing.T, a *claudecode.Adapter, onEvent func(agent.Event)) {
	t.Helper()
	deadline := time.After(120 * time.Second)
	for {
		select {
		case ev, ok := <-a.Stream():
			if !ok {
				return
			}
			onEvent(ev)
			switch ev.Kind {
			case agent.EventError:
				// A denied tool surfaces to the model, not as a stream error; a real
				// EventError is a genuine transport fault.
				t.Logf("stream error: %s", ev.Err)
			case agent.EventTurnEnd:
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn end")
		}
	}
}
