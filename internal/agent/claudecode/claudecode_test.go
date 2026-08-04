package claudecode

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Dawil/draiver/internal/agent"
)

// helperAdapter returns an adapter whose process is the test binary re-executed
// as the stream-json fake in TestHelperProcess — no real claude, no tokens.
func helperAdapter() *Adapter {
	a := New()
	a.newCmd = func(ctx context.Context, bin string, args []string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=TestHelperProcess", "--", bin}, args...)...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		return cmd
	}
	return a
}

func TestSpawnPromptStream(t *testing.T) {
	a := helperAdapter()
	id, err := a.Spawn(context.Background(), agent.SessionSpec{WorkDir: t.TempDir(), Model: "opus"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if id == "" {
		t.Fatal("Spawn returned empty session id")
	}

	sys := next(t, a)
	if sys.Kind != agent.EventSystem || sys.SessionID != id {
		t.Fatalf("first event = %+v, want system with session id %q", sys, id)
	}
	if got := a.SessionID(); got != id {
		t.Fatalf("SessionID() = %q, want %q", got, id)
	}

	if err := a.Prompt(context.Background(), "ping"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	asst := next(t, a)
	if asst.Kind != agent.EventAssistant || asst.Text != "pong" {
		t.Fatalf("want assistant pong, got %+v", asst)
	}
	end := next(t, a)
	if end.Kind != agent.EventTurnEnd || end.Turn != "success" {
		t.Fatalf("want successful turn_end, got %+v", end)
	}

	if err := a.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	assertClosed(t, a)
}

func TestInterrupt(t *testing.T) {
	a := helperAdapter()
	if _, err := a.Spawn(context.Background(), agent.SessionSpec{WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	_ = next(t, a) // system

	if err := a.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	end := next(t, a)
	if end.Kind != agent.EventTurnEnd || end.Turn != "interrupted" {
		t.Fatalf("want interrupted turn_end, got %+v", end)
	}
	if err := a.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
}

// TestPermissionRequestAndDecide drives the tool-permission callback both ways:
// the fake agent asks can_use_tool, the adapter surfaces it as an
// EventPermission, and Decide writes a control_response the agent acts on.
func TestPermissionRequestAndDecide(t *testing.T) {
	a := helperAdapter()
	ctx := context.Background()
	if _, err := a.Spawn(ctx, agent.SessionSpec{WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	_ = next(t, a) // system

	if err := a.Prompt(ctx, "please run a gated tool"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	perm := next(t, a)
	if perm.Kind != agent.EventPermission || perm.Permission == nil {
		t.Fatalf("want a permission event, got %+v", perm)
	}
	if perm.Permission.ID != "perm-1" || perm.Permission.Tool != "Bash" {
		t.Fatalf("permission = %+v, want id perm-1 tool Bash", perm.Permission)
	}

	// Adapter satisfies the optional Permissioner capability.
	var p agent.Permissioner = a
	if err := p.Decide(ctx, perm.Permission.ID, agent.Decision{Allow: true, Input: perm.Permission.Input}); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	asst := next(t, a)
	if asst.Kind != agent.EventAssistant || asst.Text != "perm:allow" {
		t.Fatalf("want assistant perm:allow after the decision, got %+v", asst)
	}
	if err := a.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
}

func TestDecideNeedsRequestID(t *testing.T) {
	a := helperAdapter()
	if _, err := a.Spawn(context.Background(), agent.SessionSpec{WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer a.Kill()
	if err := a.Decide(context.Background(), "", agent.Decision{Allow: true}); err == nil {
		t.Fatal("Decide with an empty request id should error")
	}
}

func TestResumeEchoesID(t *testing.T) {
	a := helperAdapter()
	if err := a.Resume(context.Background(), "prior-session", agent.SessionSpec{WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	sys := next(t, a)
	if sys.Kind != agent.EventSystem || sys.SessionID != "prior-session" {
		t.Fatalf("want system echoing resumed id, got %+v", sys)
	}
	_ = a.Kill()
}

func TestWorkDirRequired(t *testing.T) {
	a := helperAdapter()
	if _, err := a.Spawn(context.Background(), agent.SessionSpec{}); err == nil {
		t.Fatal("want error for missing WorkDir")
	}
}

func TestDoubleStart(t *testing.T) {
	a := helperAdapter()
	if _, err := a.Spawn(context.Background(), agent.SessionSpec{WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer a.Kill()
	if _, err := a.Spawn(context.Background(), agent.SessionSpec{WorkDir: t.TempDir()}); err == nil {
		t.Fatal("want error on second Spawn")
	}
}

func TestKillBeforeStart(t *testing.T) {
	if err := New().Kill(); err == nil {
		t.Fatal("want error killing an unstarted session")
	}
}

// TestKillWithoutDraining reaps a session whose events nobody is reading. Kill
// must not wedge on the scanner (which may be blocked mid-send), and must stay
// idempotent.
func TestKillWithoutDraining(t *testing.T) {
	a := helperAdapter()
	if _, err := a.Spawn(context.Background(), agent.SessionSpec{WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// Deliberately do not read Stream().
	done := make(chan error, 1)
	go func() { done <- a.Kill() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Kill: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Kill wedged on an undrained stream")
	}
	if err := a.Kill(); err != nil {
		t.Fatalf("second Kill (idempotent): %v", err)
	}
}

func TestBaseArgs(t *testing.T) {
	got := baseArgs(agent.SessionSpec{Model: "opus", PermissionMode: "acceptEdits", AllowedTools: []string{"Read", "Bash(echo *)"}}, "--session-id", "abc")
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"--print", "--input-format stream-json", "--output-format stream-json", "--verbose",
		"--session-id abc", "--model opus", "--permission-mode acceptEdits", "--allowedTools Read Bash(echo *)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("baseArgs missing %q in: %s", want, joined)
		}
	}
}

// next reads one event with a timeout so a wedged process fails the test
// instead of hanging it.
func next(t *testing.T, a *Adapter) agent.Event {
	t.Helper()
	select {
	case ev, ok := <-a.Stream():
		if !ok {
			t.Fatal("stream closed unexpectedly")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for event")
		return agent.Event{}
	}
}

func assertClosed(t *testing.T, a *Adapter) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-a.Stream():
			if !ok {
				return // channel drained and closed — clean exit
			}
		case <-deadline:
			t.Fatal("stream never closed after Kill")
		}
	}
}

// TestHelperProcess is not a real test: when GO_WANT_HELPER_PROCESS=1 it stands
// in for the claude binary, speaking just enough stream-json to exercise the
// adapter. Its args after "--" are the ones the adapter built.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	sessionID := "unknown"
	for i, a := range args {
		if (a == "--session-id" || a == "--resume") && i+1 < len(args) {
			sessionID = args[i+1]
		}
	}

	out := os.Stdout
	fmt.Fprintf(out, `{"type":"system","subtype":"init","session_id":%q,"model":"fake"}`+"\n", sessionID)

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, `"control_request"`) && strings.Contains(line, `"interrupt"`):
			fmt.Fprintf(out, `{"type":"result","subtype":"interrupted","is_error":false,"result":"","session_id":%q}`+"\n", sessionID)
		case strings.Contains(line, `"control_response"`):
			// The client answered our can_use_tool callback: echo the behavior back
			// as assistant text so the adapter test can observe the round-trip.
			behavior := "deny"
			if strings.Contains(line, `"allow"`) {
				behavior = "allow"
			}
			fmt.Fprintf(out, `{"type":"assistant","session_id":%q,"message":{"role":"assistant","content":[{"type":"text","text":"perm:%s"}]}}`+"\n", sessionID, behavior)
			fmt.Fprintf(out, `{"type":"result","subtype":"success","is_error":false,"result":"perm:%s","session_id":%q}`+"\n", behavior, sessionID)
		case strings.Contains(line, `"type":"user"`) && strings.Contains(line, "gated"):
			// Ask permission for a gated tool instead of answering directly.
			fmt.Fprintf(out, `{"type":"control_request","request_id":"perm-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}`+"\n")
		case strings.Contains(line, `"type":"user"`):
			fmt.Fprintf(out, `{"type":"assistant","session_id":%q,"message":{"role":"assistant","content":[{"type":"text","text":"pong"}]}}`+"\n", sessionID)
			fmt.Fprintf(out, `{"type":"result","subtype":"success","is_error":false,"result":"pong","total_cost_usd":0.001,"session_id":%q}`+"\n", sessionID)
		}
	}
	os.Exit(0)
}
