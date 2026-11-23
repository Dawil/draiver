package claudecode

import (
	"testing"

	"github.com/Dawil/draiver/internal/agent"
)

// TestNormalize_Drvctl060 covers the two stream signals drvctl-060 adds: a human
// interrupt surfaces as EventInterrupted (top-level only), and a result frame's
// origin kind is carried on the turn end.
func TestNormalize_Drvctl060(t *testing.T) {
	kinds := func(evs []agent.Event) []agent.EventKind {
		var out []agent.EventKind
		for _, e := range evs {
			out = append(out, e.Kind)
		}
		return out
	}
	has := func(evs []agent.Event, k agent.EventKind) bool {
		for _, e := range evs {
			if e.Kind == k {
				return true
			}
		}
		return false
	}

	top := normalize([]byte(`{"type":"user","session_id":"s","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}`))
	if !has(top, agent.EventInterrupted) {
		t.Fatalf("top-level interrupt line: kinds %v, want an interrupted event", kinds(top))
	}

	sub := normalize([]byte(`{"type":"user","session_id":"s","parent_tool_use_id":"toolu_9","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}`))
	if has(sub, agent.EventInterrupted) {
		t.Fatalf("a subagent's interrupt must not count as the human's: kinds %v", kinds(sub))
	}

	plain := normalize([]byte(`{"type":"user","session_id":"s","message":{"role":"user","content":[{"type":"text","text":"please continue"}]}}`))
	if has(plain, agent.EventInterrupted) {
		t.Fatalf("an ordinary user line is not an interrupt: kinds %v", kinds(plain))
	}

	res := normalize([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s","origin":{"kind":"task-notification"}}`))
	if len(res) != 1 || res[0].Kind != agent.EventTurnEnd || res[0].Origin != "task-notification" {
		t.Fatalf("result with origin: got %+v, want a turn end with Origin task-notification", res)
	}
}
